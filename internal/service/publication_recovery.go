package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/definebusiness/wtree/internal/store"
)

type publicationRecoveryDependencies struct {
	writeRawCAS      func(string, []byte, func() error) error
	writeRecoveryCAS func(string, store.RecoveryRecord, func() error) error
}

// finishReplacedPublicationFailure handles the narrow case where an atomic
// writer reports failure after replacing an authoritative file. It restores
// the exact prior bytes only while the just-published generation is still at
// the public path. A concurrent generation is preserved and described by a
// recovery record instead.
func finishReplacedPublicationFailure(original cloneFileSnapshot, attempted []byte, cause error, dataDir, projectID, workspaceID, operation, failedStep string, dependencies publicationRecoveryDependencies) error {
	restoreErr := restoreExactPublicationGeneration(original, attempted, dependencies.writeRawCAS)
	publicationErr := fmt.Errorf("%s publication failed after replacement: %w", operation, cause)
	if restoreErr == nil {
		return NewCleanRollbackError(NewError(ErrorInternal, publicationErr))
	}

	recoveryPath := filepath.Join(dataDir, "projects", projectID, "recovery", workspaceID+".json")
	recovery := store.RecoveryRecord{
		Version:         store.Version,
		ProjectID:       projectID,
		WorkspaceID:     workspaceID,
		Operation:       operation,
		FailedStep:      failedStep,
		UnrevertedSteps: []string{failedStep},
		RollbackFailures: []store.RollbackFailure{{
			Step:  failedStep,
			Error: restoreErr.Error(),
		}},
	}
	recoveryErr := publishExactRecoveryRecord(recoveryPath, recovery, dependencies.writeRecoveryCAS)
	combined := errors.Join(publicationErr, restoreErr)
	if recoveryErr != nil {
		combined = errors.Join(combined, fmt.Errorf("write recovery metadata %q: %w", recoveryPath, recoveryErr))
	} else {
		combined = errors.Join(combined, fmt.Errorf("recovery metadata: %q", recoveryPath))
	}
	return NewError(ErrorRollbackIncomplete, combined)
}

func restoreExactPublicationGeneration(original cloneFileSnapshot, attempted []byte, writeRawCAS func(string, []byte, func() error) error) error {
	if writeRawCAS == nil {
		return errors.New("exact publication restore is not configured")
	}
	owned, err := secureCloneFileSnapshot(original.path)
	if err != nil {
		return fmt.Errorf("capture failed publication generation: %w", err)
	}
	if !cloneSnapshotHasExactBytes(owned, attempted, 0o600) {
		return errors.New("publication generation changed after replacement; preserving it")
	}
	if !original.exists {
		return errors.New("prior publication generation is absent and no exact removal boundary is configured")
	}
	if err := writeRawCAS(original.path, original.data, func() error { return revalidateCloneFileSnapshot(owned) }); err != nil {
		return fmt.Errorf("restore exact prior publication generation: %w", err)
	}
	restored, err := secureCloneFileSnapshot(original.path)
	if err != nil {
		return fmt.Errorf("capture restored publication generation: %w", err)
	}
	if !cloneSnapshotHasExactBytes(restored, original.data, original.mode.Perm()) {
		return errors.New("publication restore did not retain the exact prior bytes")
	}
	return nil
}

func publishExactRecoveryRecord(path string, value store.RecoveryRecord, write func(string, store.RecoveryRecord, func() error) error) error {
	if write == nil {
		return errors.New("recovery writer is not configured")
	}
	original, err := secureCloneFileSnapshot(path)
	if err != nil {
		return err
	}
	if original.exists {
		return errors.New("recovery record already exists; preserving it")
	}
	writeErr := write(path, value, func() error { return revalidateCloneFileSnapshot(original) })
	expected, err := store.RecoveryBytes(value)
	if err != nil {
		return err
	}
	published, captureErr := secureCloneFileSnapshot(path)
	if captureErr == nil && cloneSnapshotHasExactBytes(published, expected, 0o600) {
		// The record is truthful evidence even when its directory sync reports a
		// post-replacement durability error. The caller still reports incomplete
		// rollback, so the user is not told that recovery completed.
		return nil
	}
	if writeErr != nil {
		return writeErr
	}
	if errors.Is(captureErr, os.ErrNotExist) {
		return errors.New("recovery writer did not publish a record")
	}
	return errors.Join(errors.New("recovery writer did not publish the expected generation"), captureErr)
}
