//go:build windows

package fsutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

const privateWindowsAllAccess windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

// privateWindowsPublicationAccess is deliberately narrower than the private
// creation DACL. Publication needs deletion and identity validation, not DACL
// or ownership mutation rights; keeping the ReOpenFile request minimal avoids
// turning a record transition into a security-descriptor mutation request.
const privateWindowsPublicationAccess = windows.DELETE | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE

var privateTemporarySequence atomic.Uint64
var privateWindowsBeforeIdentityReopen func(string)
var reopenPrivateWindowsHandle = func(handle windows.Handle) (windows.Handle, error) {
	return reopenWindowsHandle(handle, privateWindowsPublicationAccess)
}

func privatePathNotExist(err error) bool {
	return errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || errors.Is(err, windows.STATUS_OBJECT_PATH_NOT_FOUND) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}

type privatePath struct {
	anchor     string
	chain      []windows.Handle
	components []string
	directory  windows.Handle
	leaf       string
	user       string
}

type windowsPrivateLock struct {
	authority *privatePath
	handle    windows.Handle
	identity  windows.ByHandleFileInformation
}

type privateWindowsRenameInformation struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

type privateWindowsSetInformationFile func(windows.Handle, *windows.IO_STATUS_BLOCK, *byte, uint32, uint32) error

func containsPathSeparator(name string) bool {
	return filepath.Base(name) != name || filepath.Clean(name) != name || filepath.IsAbs(name) || name == string(filepath.Separator) || len(name) >= 2 && name[1] == ':'
}

func openPrivatePath(anchor string, components []string, leaf string, create, protectExisting bool) (*privatePath, error) {
	if !filepath.IsAbs(anchor) || filepath.Clean(anchor) != anchor {
		return nil, errors.New("private path anchor must be a cleaned absolute path")
	}
	user, err := privateWindowsEffectiveUser()
	if err != nil {
		return nil, err
	}
	pointer, err := windows.UTF16PtrFromString(anchor)
	if err != nil {
		return nil, err
	}
	current, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("open private path anchor: %w", err)
	}
	if err := validatePrivateWindowsType(current, true); err != nil {
		windows.CloseHandle(current)
		return nil, errors.Join(errors.New("unsafe private path anchor"), err)
	}
	anchorAgain, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		windows.CloseHandle(current)
		return nil, errors.Join(errors.New("reopen private path anchor identity"), err)
	}
	anchorIdentity, anchorIdentityErr := privateWindowsIdentity(current)
	anchorAgainIdentity, anchorAgainIdentityErr := privateWindowsIdentity(anchorAgain)
	windows.CloseHandle(anchorAgain)
	if anchorIdentityErr != nil || anchorAgainIdentityErr != nil || !samePrivateWindowsIdentity(anchorIdentity, anchorAgainIdentity) {
		windows.CloseHandle(current)
		return nil, errors.Join(errors.New("private path anchor identity differs"), anchorIdentityErr, anchorAgainIdentityErr)
	}
	chain := []windows.Handle{current}
	for _, component := range components {
		next, openErr := openPrivateWindowsDirectory(current, component, user, create, protectExisting)
		if openErr != nil {
			closePrivateWindowsChain(chain)
			return nil, openErr
		}
		chain = append(chain, next)
		current = next
	}
	authority := &privatePath{anchor: anchor, chain: chain, components: append([]string(nil), components...), directory: current, leaf: leaf, user: user}
	if err := authority.validateLeaf(false); err != nil {
		if !protectExisting {
			authority.close()
			return nil, err
		}
		if protectErr := authority.protectExistingLeaf(); protectErr != nil {
			authority.close()
			return nil, errors.Join(err, protectErr)
		}
		if verifyErr := authority.validateLeaf(false); verifyErr != nil {
			authority.close()
			return nil, verifyErr
		}
	}
	return authority, nil
}

func closePrivateWindowsChain(chain []windows.Handle) error {
	var result error
	for index := len(chain) - 1; index >= 0; index-- {
		if chain[index] != windows.InvalidHandle && chain[index] != 0 {
			result = errors.Join(result, windows.CloseHandle(chain[index]))
		}
	}
	return result
}

func privateWindowsEffectiveUser() (string, error) {
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return "", errors.Join(errors.New("read effective Windows token user"), err)
	}
	return user.User.Sid.String(), nil
}

func openPrivateWindowsDirectory(parent windows.Handle, name, user string, create, protectExisting bool) (windows.Handle, error) {
	access := windows.ACCESS_MASK(windows.FILE_READ_ATTRIBUTES | windows.FILE_LIST_DIRECTORY | windows.READ_CONTROL | windows.SYNCHRONIZE)
	if protectExisting {
		access |= windows.WRITE_DAC
	}
	handle, err := openPrivateWindowsRelative(parent, name,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, nil)
	if err != nil && create {
		descriptor, descriptorErr := privateWindowsDescriptor(user, true)
		if descriptorErr != nil {
			return windows.InvalidHandle, descriptorErr
		}
		handle, err = openPrivateWindowsRelative(parent, name,
			privateWindowsAllAccess,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_CREATE,
			windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, descriptor)
		if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
			handle, err = openPrivateWindowsRelative(parent, name,
				access,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
				windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, nil)
		}
	}
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("open private directory %q: %w", name, err)
	}
	if err := validatePrivateWindowsType(handle, true); err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, errors.Join(errors.New("unsafe private directory"), err)
	}
	if err := validatePrivateWindowsRelativeIdentity(parent, name, handle, true); err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	if err := validatePrivateWindowsHandle(handle, user, true); err != nil {
		if !protectExisting {
			windows.CloseHandle(handle)
			return windows.InvalidHandle, errors.Join(errors.New("unsafe private directory"), err)
		}
		if protectErr := protectPrivateWindowsHandle(handle, user, true); protectErr != nil {
			windows.CloseHandle(handle)
			return windows.InvalidHandle, errors.Join(err, protectErr)
		}
	}
	return handle, nil
}

func protectPrivateWindowsHandle(handle windows.Handle, user string, directory bool) error {
	if err := validatePrivateWindowsType(handle, directory); err != nil {
		return err
	}
	current, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, defaulted, err := current.Owner()
	if err != nil || owner == nil || defaulted || owner.String() != user {
		return errors.Join(errors.New("private path owner differs from effective user"), err)
	}
	descriptor, err := privateWindowsDescriptor(user, directory)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return err
	}
	return validatePrivateWindowsHandle(handle, user, directory)
}

func openPrivateWindowsRelative(parent windows.Handle, name string, access windows.ACCESS_MASK, share, disposition, options uint32, descriptor *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      parent,
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: descriptor,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	allocation := int64(0)
	err = windows.NtCreateFile(&handle, uint32(access), attributes, &status, &allocation, windows.FILE_ATTRIBUTE_NORMAL, share, disposition, options, 0, 0)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func privateWindowsDescriptor(user string, directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	flags := ""
	if directory {
		flags = "OICI"
	}
	return windows.SecurityDescriptorFromString("O:" + user + "D:P(A;" + flags + ";FA;;;" + user + ")")
}

func validatePrivateWindowsType(handle windows.Handle, directory bool) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	isDirectory := information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != directory || information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("private path has unsafe type or reparse attributes")
	}
	return nil
}

func validatePrivateWindowsHandle(handle windows.Handle, user string, directory bool) error {
	if err := validatePrivateWindowsType(handle, directory); err != nil {
		return err
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read private path security: %w", err)
	}
	return validatePrivateWindowsSecurityDescriptor(descriptor, user, directory)
}

func validatePrivateWindowsSecurityDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, user string, directory bool) error {
	if descriptor == nil {
		return errors.New("private path security descriptor is absent")
	}
	control, _, err := descriptor.Control()
	protected := control&windows.SE_DACL_PROTECTED != 0
	if err != nil || control&windows.SE_DACL_PRESENT == 0 || directory && !protected {
		return errors.Join(errors.New("private path DACL is absent or unprotected"), err)
	}
	owner, defaulted, err := descriptor.Owner()
	if err != nil || owner == nil || defaulted || owner.String() != user {
		return errors.Join(errors.New("private path owner differs from effective user"), err)
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil || dacl == nil || defaulted {
		return errors.Join(errors.New("private path DACL is null or defaulted"), err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil {
		return errors.Join(errors.New("private path DACL has no allow entry"), err)
	}
	var extra *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 1, &extra); err == nil {
		return errors.New("private path DACL contains more than one entry")
	} else if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return fmt.Errorf("inspect private path DACL entry count: %w", err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != privateWindowsAllAccess {
		return errors.New("private path DACL entry has unsafe type or access")
	}
	if directory {
		want := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
		if ace.Header.AceFlags&want != want || ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			return errors.New("private directory DACL entry has unsafe inheritance")
		}
	} else if protected {
		if ace.Header.AceFlags != 0 {
			return errors.New("protected private leaf DACL entry has unsafe inheritance")
		}
	} else if ace.Header.AceFlags&windows.INHERITED_ACE == 0 || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
		return errors.New("unprotected private leaf DACL is not inherited from its protected parent")
	}
	trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if trustee == nil || trustee.String() != user {
		return errors.New("private path DACL trustee differs from effective user")
	}
	return nil
}

func privateWindowsIdentity(handle windows.Handle) (windows.ByHandleFileInformation, error) {
	var information windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(handle, &information)
	return information, err
}

func samePrivateWindowsIdentity(left, right windows.ByHandleFileInformation) bool {
	return left.VolumeSerialNumber == right.VolumeSerialNumber && left.FileIndexHigh == right.FileIndexHigh && left.FileIndexLow == right.FileIndexLow
}

func validatePrivateWindowsRelativeIdentity(parent windows.Handle, name string, expected windows.Handle, directory bool) error {
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	if privateWindowsBeforeIdentityReopen != nil {
		privateWindowsBeforeIdentityReopen(name)
	}
	actual, err := openPrivateWindowsRelative(parent, name, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, options, nil)
	if err != nil {
		return errors.Join(errors.New("reopen private path identity"), err)
	}
	defer windows.CloseHandle(actual)
	expectedIdentity, expectedErr := privateWindowsIdentity(expected)
	actualIdentity, actualErr := privateWindowsIdentity(actual)
	if expectedErr != nil || actualErr != nil || !samePrivateWindowsIdentity(expectedIdentity, actualIdentity) {
		return errors.Join(errors.New("private path handle identity differs from current name"), expectedErr, actualErr)
	}
	return nil
}

func (path *privatePath) close() error {
	if path == nil || len(path.chain) == 0 {
		return nil
	}
	err := closePrivateWindowsChain(path.chain)
	path.chain = nil
	path.directory = windows.InvalidHandle
	return err
}

func (path *privatePath) validateDirectory() error {
	if path == nil || path.directory == windows.InvalidHandle || len(path.chain) != len(path.components)+1 || path.chain[len(path.chain)-1] != path.directory {
		return os.ErrInvalid
	}
	pointer, err := windows.UTF16PtrFromString(path.anchor)
	if err != nil {
		return err
	}
	anchorCurrent, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return errors.Join(errors.New("private path anchor generation changed"), err)
	}
	retainedAnchorIdentity, retainedAnchorErr := privateWindowsIdentity(path.chain[0])
	currentAnchorIdentity, currentAnchorErr := privateWindowsIdentity(anchorCurrent)
	anchorTypeErr := validatePrivateWindowsType(anchorCurrent, true)
	anchorCloseErr := windows.CloseHandle(anchorCurrent)
	if retainedAnchorErr != nil || currentAnchorErr != nil || anchorTypeErr != nil || anchorCloseErr != nil || !samePrivateWindowsIdentity(retainedAnchorIdentity, currentAnchorIdentity) {
		return errors.Join(errors.New("private path anchor generation changed"), retainedAnchorErr, currentAnchorErr, anchorTypeErr, anchorCloseErr)
	}
	for index, component := range path.components {
		retained := path.chain[index+1]
		if err := validatePrivateWindowsHandle(retained, path.user, true); err != nil {
			return errors.Join(errors.New("unsafe retained private directory"), err)
		}
		if err := validatePrivateWindowsRelativeIdentity(path.chain[index], component, retained, true); err != nil {
			return errors.Join(errors.New("private directory generation changed"), err)
		}
	}
	return nil
}

func (path *privatePath) openLeaf(access windows.ACCESS_MASK, share, disposition uint32, descriptor *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
	if err := path.validateDirectory(); err != nil {
		return windows.InvalidHandle, errors.Join(errPrivateDirectoryAuthority, errors.New("unsafe retained private directory"), err)
	}
	handle, err := openPrivateWindowsRelative(path.directory, path.leaf, access, share, disposition,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, descriptor)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := validatePrivateWindowsHandle(handle, path.user, false); err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, errors.Join(errors.New("unsafe private leaf"), err)
	}
	if err := validatePrivateWindowsRelativeIdentity(path.directory, path.leaf, handle, false); err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func (path *privatePath) protectExistingLeaf() error {
	if err := path.validateDirectory(); err != nil {
		return err
	}
	handle, err := openPrivateWindowsRelative(path.directory, path.leaf,
		windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.WRITE_DAC|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if err := validatePrivateWindowsRelativeIdentity(path.directory, path.leaf, handle, false); err != nil {
		return err
	}
	return protectPrivateWindowsHandle(handle, path.user, false)
}

func (path *privatePath) validateLeaf(required bool) error {
	handle, err := path.openLeaf(windows.FILE_GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, nil)
	if err == nil {
		return windows.CloseHandle(handle)
	}
	if !required && !errors.Is(err, errPrivateDirectoryAuthority) && (errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND)) {
		return nil
	}
	return err
}

func (path *privatePath) readFile() ([]byte, error) {
	return path.readFileNamed(path.leaf)
}

func (path *privatePath) readFileNamed(name string) ([]byte, error) {
	copy := *path
	copy.leaf = name
	handle, err := copy.openLeaf(windows.FILE_GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, nil)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	if file == nil {
		windows.CloseHandle(handle)
		return nil, errors.New("adopt private leaf handle")
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	validationErr := path.validateDirectory()
	if readErr != nil || closeErr != nil || validationErr != nil {
		return nil, errors.Join(readErr, closeErr, validationErr)
	}
	return data, nil
}

func (path *privatePath) readDirectory() ([]string, error) {
	if err := path.validateDirectory(); err != nil {
		return nil, err
	}
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(process, path.directory, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(duplicate), "private-directory")
	if directory == nil {
		windows.CloseHandle(duplicate)
		return nil, errors.New("adopt private directory enumeration handle")
	}
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !validPrivateName(entry.Name()) {
			return nil, errors.New("unsafe private directory entry name")
		}
		copy := *path
		copy.leaf = entry.Name()
		handle, err := copy.openLeaf(windows.FILE_GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, nil)
		if err != nil {
			return nil, err
		}
		if err := windows.CloseHandle(handle); err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	if err := path.validateDirectory(); err != nil {
		return nil, err
	}
	return names, nil
}

func (path *privatePath) writeFileAtomic(data []byte, mode os.FileMode, hook AtomicStepHook) error {
	if mode.Perm() != 0o600 {
		return errors.New("private leaf mode must be 0600")
	}
	if err := path.validateLeaf(false); err != nil {
		return err
	}
	if err := atomicStep(hook, "create-temp"); err != nil {
		return err
	}
	descriptor, err := privateWindowsDescriptor(path.user, false)
	if err != nil {
		return err
	}
	name := fmt.Sprintf(".%s-%d-%d", path.leaf, os.Getpid(), privateTemporarySequence.Add(1))
	writerHandle, err := openPrivateWindowsRelative(path.directory, name,
		windows.GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, descriptor)
	if err != nil {
		return err
	}
	writer := os.NewFile(uintptr(writerHandle), name)
	if writer == nil {
		windows.CloseHandle(writerHandle)
		return errors.New("adopt private temporary handle")
	}
	renamed, disposeTemporary := false, true
	publisherHandle := windows.InvalidHandle
	var publisher *os.File
	defer func() {
		if !renamed && disposeTemporary {
			cleanup := publisherHandle
			if cleanup == windows.InvalidHandle && writer != nil {
				cleanup, _ = reopenPrivateWindowsHandle(writerHandle)
			}
			if cleanup != windows.InvalidHandle {
				_ = removePrivateWindowsHandle(cleanup)
				if cleanup != publisherHandle {
					_ = windows.CloseHandle(cleanup)
				}
			}
		}
		if publisher != nil {
			_ = publisher.Close()
		}
		if writer != nil {
			_ = writer.Close()
		}
	}()
	if err := validatePrivateWindowsHandle(writerHandle, path.user, false); err != nil {
		return err
	}
	if err := atomicStep(hook, "write"); err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := atomicStep(hook, "sync"); err != nil {
		return err
	}
	if err := Sync(writer); err != nil {
		return err
	}
	if err := atomicStep(hook, "close"); err != nil {
		return err
	}
	if err := atomicStep(hook, "before-rename"); err != nil {
		return err
	}
	if err := path.validateDirectory(); err != nil {
		return err
	}
	publisherHandle, err = reopenPrivateWindowsHandle(writerHandle)
	if err != nil {
		return err
	}
	publisher = os.NewFile(uintptr(publisherHandle), name)
	if publisher == nil {
		windows.CloseHandle(publisherHandle)
		publisherHandle = windows.InvalidHandle
		return errors.New("adopt private publication handle")
	}
	if err := writer.Close(); err != nil {
		writer = nil
		return err
	}
	writer = nil
	if err := renamePrivateWindowsHandle(publisherHandle, path.directory, path.leaf); err != nil {
		return err
	}
	renamed = true
	if err := validatePrivateWindowsRelativeIdentity(path.directory, path.leaf, publisherHandle, false); err != nil {
		return &postReplacementError{Err: err}
	}
	if err := path.validateLeaf(true); err != nil {
		return &postReplacementError{Err: err}
	}
	if err := publisher.Close(); err != nil {
		publisher = nil
		return &postReplacementError{Err: fmt.Errorf("close private publication handle: %w", err)}
	}
	publisher = nil
	if err := atomicStep(hook, "dir-sync"); err != nil {
		return &postReplacementError{Err: err}
	}
	if err := path.validateDirectory(); err != nil {
		return &postReplacementError{Err: err}
	}
	return nil
}

func renamePrivateWindowsHandle(handle, root windows.Handle, name string) error {
	return renamePrivateWindowsHandleWithInformation(handle, root, name, windows.FILE_RENAME_REPLACE_IF_EXISTS, windows.FileRenameInformation, windows.NtSetInformationFile)
}

func renamePrivateWindowsHandleWithInformation(handle, root windows.Handle, name string, flags, class uint32, setInformation privateWindowsSetInformationFile) error {
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	length := len(encoded)*2 - 2
	var layout privateWindowsRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(layout.FileName))+length)
	information := (*privateWindowsRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.Flags = flags
	information.RootDirectory = root
	information.FileNameLength = uint32(length)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&information.FileName[0]))[:length/2:length/2], encoded)
	var status windows.IO_STATUS_BLOCK
	return setInformation(handle, &status, &buffer[0], uint32(len(buffer)), class)
}

func removePrivateWindowsHandle(handle windows.Handle) error {
	information := struct{ DeleteFile byte }{DeleteFile: 1}
	return windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&information)), uint32(unsafe.Sizeof(information)))
}

func (path *privatePath) remove(hook AtomicStepHook) error {
	handle, err := path.openLeaf(windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN, nil)
	if !errors.Is(err, errPrivateDirectoryAuthority) && (errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND)) {
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if err := atomicStep(hook, "before-quarantine"); err != nil {
		return err
	}
	if err := validatePrivateWindowsRelativeIdentity(path.directory, path.leaf, handle, false); err != nil {
		return errors.Join(ErrPrivateRemovalAmbiguous, err)
	}
	if err := validatePrivateWindowsHandle(handle, path.user, false); err != nil {
		return errors.Join(ErrPrivateRemovalAmbiguous, err)
	}
	if err := path.validateDirectory(); err != nil {
		return errors.Join(ErrPrivateRemovalAmbiguous, err)
	}
	return removePrivateWindowsHandle(handle)
}

func (path *privatePath) syncDirectory() error {
	return path.validateDirectory()
}

func (path *privatePath) tryLock() (*windowsPrivateLock, error) {
	descriptor, err := privateWindowsDescriptor(path.user, false)
	if err != nil {
		return nil, err
	}
	handle, err := path.openLeaf(windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN_IF, descriptor)
	if err != nil {
		return nil, err
	}
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); err != nil {
		windows.CloseHandle(handle)
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, ErrPrivateLockHeld
		}
		return nil, err
	}
	identity, err := privateWindowsIdentity(handle)
	if err != nil {
		windows.UnlockFileEx(handle, 0, 1, 0, &windows.Overlapped{})
		windows.CloseHandle(handle)
		return nil, err
	}
	if err := path.validateDirectory(); err != nil {
		windows.UnlockFileEx(handle, 0, 1, 0, &windows.Overlapped{})
		windows.CloseHandle(handle)
		return nil, err
	}
	return &windowsPrivateLock{authority: path, handle: handle, identity: identity}, nil
}

func (lock *windowsPrivateLock) Unlock() error {
	if lock == nil || lock.handle == windows.InvalidHandle {
		return nil
	}
	var result error
	if err := lock.authority.validateDirectory(); err != nil {
		result = errors.Join(result, errors.New("private lock directory generation changed during lease"), err)
	}
	if err := validatePrivateWindowsRelativeIdentity(lock.authority.directory, lock.authority.leaf, lock.handle, false); err != nil {
		result = errors.Join(result, errors.New("private lock leaf generation changed during lease"), err)
	}
	result = errors.Join(result, windows.UnlockFileEx(lock.handle, 0, 1, 0, &windows.Overlapped{}), windows.CloseHandle(lock.handle), lock.authority.close())
	lock.handle = windows.InvalidHandle
	return result
}
