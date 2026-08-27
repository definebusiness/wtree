package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// AggregateStatus is the command-neutral lifecycle of one repository result.
// It intentionally contains no command-specific actions or storage details.
type AggregateStatus string

const (
	AggregateStatusPlanned   AggregateStatus = "planned"
	AggregateStatusCompleted AggregateStatus = "completed"
	AggregateStatusFailed    AggregateStatus = "failed"
	AggregateStatusCanceled  AggregateStatus = "canceled"
)

// AggregateFailure is a stable, bounded and redacted command-neutral failure.
type AggregateFailure struct {
	Code    ErrorKind `json:"code"`
	Message string    `json:"message"`
}

// NewAggregateFailure converts an operational cause to a render-safe fact.
func NewAggregateFailure(code ErrorKind, cause error) (AggregateFailure, error) {
	if !validCloneResultErrorKind(code) {
		return AggregateFailure{}, fmt.Errorf("invalid aggregate failure code %q", code)
	}
	if cause == nil {
		return AggregateFailure{}, errors.New("aggregate failure requires a cause")
	}
	message := boundedRedactedDiagnostic(cause.Error())
	if message == "" {
		return AggregateFailure{}, errors.New("aggregate failure has an empty diagnostic")
	}
	return AggregateFailure{Code: code, Message: message}, nil
}

// AggregateOutput preserves separately bounded streams for a repository
// command. Output is intentionally not interpreted by this shared contract.
type AggregateOutput struct {
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
	ExitCode        int
}

// AggregateRepositoryFact is the common observation/result for one managed
// repository. Repositories are stored in deterministic parent-first order.
type AggregateRepositoryFact struct {
	ID             string
	ParentID       string
	Mount          string
	Path           string
	Branch         string
	Head           string
	ObservedCommit string
	Status         AggregateStatus
	Output         AggregateOutput
	Failure        *AggregateFailure
}

// AggregateFacts is an immutable, ordered set of repository facts for later
// aggregate commands. It is deliberately non-public and has no JSON shape.
type AggregateFacts struct{ repositories []AggregateRepositoryFact }

// NewAggregateFacts validates and defensively copies parent-first facts.
func NewAggregateFacts(repositories []AggregateRepositoryFact) (AggregateFacts, error) {
	if len(repositories) == 0 {
		return AggregateFacts{}, errors.New("aggregate facts require repositories")
	}
	copyOfRepositories := copyAggregateRepositoryFacts(repositories)
	seen := make(map[string]struct{}, len(copyOfRepositories))
	for _, repository := range copyOfRepositories {
		if err := validateAggregateRepositoryFact(repository, seen); err != nil {
			return AggregateFacts{}, err
		}
		seen[repository.ID] = struct{}{}
	}
	return AggregateFacts{repositories: copyOfRepositories}, nil
}

// Repositories returns a defensive copy in the fixed parent-first order.
func (facts AggregateFacts) Repositories() []AggregateRepositoryFact {
	return copyAggregateRepositoryFacts(facts.repositories)
}

func copyAggregateRepositoryFacts(repositories []AggregateRepositoryFact) []AggregateRepositoryFact {
	copyOfRepositories := append([]AggregateRepositoryFact(nil), repositories...)
	for index := range copyOfRepositories {
		if repositories[index].Failure == nil {
			continue
		}
		failure := *repositories[index].Failure
		copyOfRepositories[index].Failure = &failure
	}
	return copyOfRepositories
}

func validateAggregateRepositoryFact(repository AggregateRepositoryFact, seen map[string]struct{}) error {
	if repository.ID == "" {
		return errors.New("aggregate repository ID is required")
	}
	if _, exists := seen[repository.ID]; exists {
		return fmt.Errorf("aggregate repository %q is duplicated", repository.ID)
	}
	if repository.ParentID != "" {
		if _, exists := seen[repository.ParentID]; !exists {
			return fmt.Errorf("aggregate repository %q is not parent-first", repository.ID)
		}
	}
	if repository.Mount == "" || strings.Contains(repository.Mount, "\x00") {
		return fmt.Errorf("aggregate repository %q mount is required", repository.ID)
	}
	if !filepath.IsAbs(repository.Path) || filepath.Clean(repository.Path) != repository.Path {
		return fmt.Errorf("aggregate repository %q path must be absolute and clean", repository.ID)
	}
	if repository.Branch != "" && strings.Contains(repository.Branch, "\x00") {
		return fmt.Errorf("aggregate repository %q branch is invalid", repository.ID)
	}
	if repository.Head != "" && !aggregateObjectID(repository.Head) || repository.ObservedCommit != "" && !aggregateObjectID(repository.ObservedCommit) {
		return fmt.Errorf("aggregate repository %q has an invalid commit observation", repository.ID)
	}
	switch repository.Status {
	case AggregateStatusPlanned, AggregateStatusCompleted, AggregateStatusCanceled:
		if repository.Failure != nil {
			return fmt.Errorf("aggregate repository %q has failure outside failed status", repository.ID)
		}
	case AggregateStatusFailed:
		if repository.Failure == nil {
			return fmt.Errorf("aggregate repository %q failed without failure", repository.ID)
		}
		if !validCloneResultErrorKind(repository.Failure.Code) || repository.Failure.Message == "" || boundedRedactedDiagnostic(repository.Failure.Message) != repository.Failure.Message {
			return fmt.Errorf("aggregate repository %q has invalid failure", repository.ID)
		}
	default:
		return fmt.Errorf("aggregate repository %q has invalid status %q", repository.ID, repository.Status)
	}
	return nil
}

func aggregateObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

const (
	directProcessRetainedStreamBytes = 32 * 1024
	directProcessInspectionBytes     = 64 * 1024
	directProcessTruncationMarker    = "\n[wtree: output truncated]\n"
	directProcessCleanupTimeout      = time.Second
)

var directProcessTerminate = terminateDirectProcess

// DirectProcessRequest describes one direct executable invocation. Arguments
// are passed exactly as an argv array; this contract never invokes a shell.
type DirectProcessRequest struct {
	Program     string
	Args        []string
	Directory   string
	Environment []string
}

// DirectProcessResult captures independently bounded, redacted streams. A
// non-zero process exit is represented by ExitCode with a nil error so an
// aggregate command can retain per-repository results.
type DirectProcessResult struct {
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
	ExitCode        int
	started         bool
}

// RunDirectProcess executes one program without a shell or inherited hostile
// environment. Context cancellation and process-start failures return errors.
func RunDirectProcess(ctx context.Context, request DirectProcessRequest) (DirectProcessResult, error) {
	if err := ctx.Err(); err != nil {
		return DirectProcessResult{}, err
	}
	if request.Program == "" || request.Directory == "" || !filepath.IsAbs(request.Directory) || filepath.Clean(request.Directory) != request.Directory {
		return DirectProcessResult{}, errors.New("invalid direct process request")
	}
	environment, err := sanitizedDirectProcessEnvironment(request.Environment)
	if err != nil {
		return DirectProcessResult{}, err
	}
	stdout, stderr := &boundedProcessStream{}, &boundedProcessStream{}
	command := exec.Command(request.Program, append([]string(nil), request.Args...)...)
	command.Dir = request.Directory
	command.Env = environment
	configureDirectProcess(command)
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return DirectProcessResult{}, fmt.Errorf("capture direct process stdout: %w", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return DirectProcessResult{}, fmt.Errorf("capture direct process stderr: %w", err)
	}
	command.Stdout = stdoutWriter
	command.Stderr = stderrWriter
	if err := command.Start(); err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		return DirectProcessResult{}, fmt.Errorf("start direct process: %w", err)
	}
	// These are caller-owned pipes, not Cmd.StdoutPipe/StdErrPipe. Closing the
	// parent's writer copies after Start lets readers observe EOF only after the
	// process tree releases every inherited writer, and permits Wait to proceed
	// without violating os/exec's pipe-ordering contract.
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	captureDone := make(chan error, 2)
	go copyDirectProcessStream(stdout, stdoutReader, captureDone)
	go copyDirectProcessStream(stderr, stderrReader, captureDone)
	runErr, captureErr, cleanupErr := waitDirectProcess(ctx, command, captureDone, stdoutReader, stderrReader)
	stdoutOutput, stdoutTruncated := stdout.Render()
	stderrOutput, stderrTruncated := stderr.Render()
	result := DirectProcessResult{
		Stdout: stdoutOutput, Stderr: stderrOutput,
		StdoutTruncated: stdoutTruncated, StderrTruncated: stderrTruncated,
		started: true,
	}
	var exitError *exec.ExitError
	if errors.As(runErr, &exitError) {
		result.ExitCode = exitError.ExitCode()
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if cleanupErr != nil {
			return result, fmt.Errorf("%w: direct process cleanup: %v", ctxErr, cleanupErr)
		}
		return result, ctxErr
	}
	if captureErr != nil {
		return result, fmt.Errorf("capture direct process output: %w", captureErr)
	}
	if runErr == nil {
		return result, nil
	}
	if exitError != nil {
		return result, nil
	}
	return result, fmt.Errorf("run direct process: %w", runErr)
}

type boundedProcessStream struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	total   int64
	scanner directProcessRedactionScanner
}

func (stream *boundedProcessStream) Write(value []byte) (int, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	stream.scanner.consume(value)
	stream.total += int64(len(value))
	stream.head = appendDirectProcessHead(stream.head, value)
	stream.tail = appendDirectProcessTail(stream.tail, value)
	stream.scanner.prune(stream.total)
	return len(value), nil
}

func (stream *boundedProcessStream) String() string {
	output, _ := stream.Render()
	return output
}

func (stream *boundedProcessStream) Truncated() bool {
	_, truncated := stream.Render()
	return truncated
}

// Render returns the redacted bounded output and whether the returned value,
// rather than the raw stream, was truncated. A complete raw inspection window
// can become short enough after redaction to be returned without a marker.
func (stream *boundedProcessStream) Render() (string, bool) {
	return renderDirectProcessOutput(stream.snapshot())
}

func appendDirectProcessHead(head, value []byte) []byte {
	remaining := directProcessInspectionBytes - len(head)
	if remaining <= 0 || len(value) == 0 {
		return head
	}
	if remaining > len(value) {
		remaining = len(value)
	}
	if cap(head) < directProcessInspectionBytes {
		bounded := make([]byte, len(head), directProcessInspectionBytes)
		copy(bounded, head)
		head = bounded
	}
	return append(head, value[:remaining]...)
}

func appendDirectProcessTail(tail, value []byte) []byte {
	if len(value) == 0 {
		return tail
	}
	if len(value) >= directProcessInspectionBytes {
		bounded := make([]byte, directProcessInspectionBytes, directProcessInspectionBytes)
		copy(bounded, value[len(value)-directProcessInspectionBytes:])
		return bounded
	}
	if cap(tail) < directProcessInspectionBytes {
		bounded := make([]byte, len(tail), directProcessInspectionBytes)
		copy(bounded, tail)
		tail = bounded
	}
	if len(tail)+len(value) <= directProcessInspectionBytes {
		return append(tail, value...)
	}
	bounded := make([]byte, directProcessInspectionBytes, directProcessInspectionBytes)
	keep := directProcessInspectionBytes - len(value)
	copy(bounded, tail[len(tail)-keep:])
	copy(bounded[keep:], value)
	return bounded
}

func copyDirectProcessStream(destination *boundedProcessStream, source io.ReadCloser, done chan<- error) {
	_, err := io.Copy(destination, source)
	closeErr := source.Close()
	if err == nil {
		err = closeErr
	}
	done <- err
}

func waitDirectProcess(ctx context.Context, command *exec.Cmd, captureDone <-chan error, stdout, stderr io.Closer) (error, error, error) {
	var captureErr error
	capturesRemaining := 2
	for capturesRemaining > 0 {
		select {
		case err := <-captureDone:
			capturesRemaining--
			if captureErr == nil && err != nil {
				captureErr = err
			}
		case <-ctx.Done():
			return waitCanceledDirectProcess(command, captureDone, capturesRemaining, captureErr, stdout, stderr)
		}
	}
	// With caller-owned pipes, EOF proves both streams (including inherited
	// writers) have drained. Wait is deliberately last on normal completion.
	return command.Wait(), captureErr, nil
}

func waitCanceledDirectProcess(command *exec.Cmd, captureDone <-chan error, capturesRemaining int, captureErr error, stdout, stderr io.Closer) (error, error, error) {
	cleanupErr := stopDirectProcess(command)
	processDone := make(chan error, 1)
	go func() { processDone <- command.Wait() }()
	timer := time.NewTimer(directProcessCleanupTimeout)
	defer timer.Stop()
	var runErr error
	processComplete := false
	for !processComplete || capturesRemaining > 0 {
		select {
		case err := <-processDone:
			runErr = err
			processComplete = true
		case err := <-captureDone:
			capturesRemaining--
			if captureErr == nil && err != nil {
				captureErr = err
			}
		case <-timer.C:
			_ = stdout.Close()
			_ = stderr.Close()
			return runErr, captureErr, appendProcessCleanupError(cleanupErr, "process or owned output pipes did not close", directProcessCleanupTimeout)
		}
	}
	return runErr, captureErr, cleanupErr
}

func appendProcessCleanupError(existing error, message string, timeout time.Duration) error {
	if existing == nil {
		return fmt.Errorf("%s within %s", message, timeout)
	}
	return fmt.Errorf("%v; %s within %s", existing, message, timeout)
}

func stopDirectProcess(command *exec.Cmd) error {
	if err := directProcessTerminate(command); err == nil {
		return nil
	} else if retryErr := directProcessTerminate(command); retryErr != nil {
		// The injectable attempts model normal termination and escalation. A
		// final platform primitive is still attempted so a testable cleanup
		// failure does not itself strand the waiter or inherited pipe readers.
		if forceErr := terminateDirectProcess(command); forceErr != nil {
			return fmt.Errorf("terminate direct process: %v; escalation: %v; final force termination: %w", err, retryErr, forceErr)
		}
		return fmt.Errorf("terminate direct process: %v; escalation: %v; final force termination succeeded", err, retryErr)
	}
	return nil
}

func redactDirectProcessOutput(stream *boundedProcessStream) string {
	output, _ := stream.Render()
	return output
}

func renderDirectProcessOutput(snapshot directProcessStreamSnapshot) (string, bool) {
	if snapshot.total <= directProcessInspectionBytes*2 {
		complete := snapshot.reconstruct()
		return cropRedactedDirectProcessOutput(redactDirectProcessVisibleOutput(string(complete)))
	}
	head := redactDirectProcessWindow(0, snapshot.head, snapshot.headSpans)
	tailStart := snapshot.total - int64(len(snapshot.tail))
	tail := redactDirectProcessWindow(tailStart, snapshot.tail, snapshot.tailSpans)
	return firstDirectProcessBytes(head, directProcessRetainedStreamBytes) + directProcessTruncationMarker + lastDirectProcessBytes(tail, directProcessRetainedStreamBytes), true
}

var directProcessSensitiveQuery = regexp.MustCompile(`(?i)([?&](?:token|access_token|password|passwd|secret|api_key|apikey|auth|authorization)=)[^&#\s'"]*`)

func redactDirectProcessVisibleOutput(value string) string {
	value = credentialURLPattern.ReplaceAllString(value, `${1}REDACTED@`)
	return directProcessSensitiveQuery.ReplaceAllString(value, `${1}[REDACTED]`)
}

type directProcessStreamSnapshot struct {
	head, tail           []byte
	total                int64
	headSpans, tailSpans []directProcessRedactionSpan
}

func (stream *boundedProcessStream) snapshot() directProcessStreamSnapshot {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	stream.scanner.finish()
	return directProcessStreamSnapshot{
		head: append([]byte(nil), stream.head...), tail: append([]byte(nil), stream.tail...), total: stream.total,
		headSpans: append([]directProcessRedactionSpan(nil), stream.scanner.headSpans...), tailSpans: append([]directProcessRedactionSpan(nil), stream.scanner.activeTailSpans()...),
	}
}

func (snapshot directProcessStreamSnapshot) reconstruct() []byte {
	if snapshot.total <= directProcessInspectionBytes {
		return append([]byte(nil), snapshot.head...)
	}
	overlap := int(int64(len(snapshot.head)+len(snapshot.tail)) - snapshot.total)
	complete := append([]byte(nil), snapshot.head...)
	return append(complete, snapshot.tail[overlap:]...)
}

func cropRedactedDirectProcessOutput(value string) (string, bool) {
	if len(value) <= directProcessInspectionBytes {
		return value, false
	}
	return firstDirectProcessBytes(value, directProcessRetainedStreamBytes) + directProcessTruncationMarker + lastDirectProcessBytes(value, directProcessRetainedStreamBytes), true
}

func firstDirectProcessBytes(value string, count int) string {
	if len(value) <= count {
		return value
	}
	return value[:count]
}

func lastDirectProcessBytes(value string, count int) string {
	if len(value) <= count {
		return value
	}
	return value[len(value)-count:]
}

type directProcessRedactionSpan struct{ start, end int64 }

func redactDirectProcessWindow(start int64, value []byte, spans []directProcessRedactionSpan) string {
	end := start + int64(len(value))
	result := make([]byte, 0, len(value))
	position := start
	for _, span := range normalizeDirectProcessSpans(spans) {
		if span.start == span.end {
			if span.start < start || span.start >= end {
				continue
			}
		} else if span.end <= position || span.start >= end {
			continue
		}
		left := maxDirectProcessOffset(position, span.start)
		if left > position {
			result = append(result, value[position-start:left-start]...)
		}
		result = append(result, "[REDACTED]"...)
		position = minDirectProcessOffset(end, span.end)
	}
	if position < end {
		result = append(result, value[position-start:]...)
	}
	return string(result)
}

// normalizeDirectProcessSpans establishes the renderer invariant: spans are
// ordered by start and never overlap or nest. A userinfo span may be discovered
// after a sensitive query span inside that same authority, so discovery order
// itself cannot be used for rendering.
func normalizeDirectProcessSpans(spans []directProcessRedactionSpan) []directProcessRedactionSpan {
	if len(spans) < 2 {
		return spans
	}
	normalized := append([]directProcessRedactionSpan(nil), spans...)
	sort.Slice(normalized, func(left, right int) bool {
		if normalized[left].start == normalized[right].start {
			return normalized[left].end > normalized[right].end
		}
		return normalized[left].start < normalized[right].start
	})
	write := 0
	for _, span := range normalized {
		if span.end < span.start {
			continue
		}
		if write > 0 && span.start <= normalized[write-1].end {
			if span.end > normalized[write-1].end {
				normalized[write-1].end = span.end
			}
			continue
		}
		normalized[write] = span
		write++
	}
	return normalized[:write]
}

func minDirectProcessOffset(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
func maxDirectProcessOffset(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

type directProcessRedactionScanner struct {
	position                                             int64
	state, authorityQueryState                           directProcessScanState
	authorityStart, valueStart, authorityQueryValueStart int64
	queryKey, authorityQueryKey                          []byte
	headSpans, tailSpans                                 []directProcessRedactionSpan
	tailSpanStart                                        int
}

type directProcessScanState uint8

const (
	directProcessScanNormal directProcessScanState = iota
	directProcessScanScheme
	directProcessScanSlashOne
	directProcessScanSlashTwo
	directProcessScanAuthority
	directProcessScanQueryKey
	directProcessScanSensitiveValue
)

func (scanner *directProcessRedactionScanner) consume(value []byte) {
	for _, character := range value {
		scanner.consumeByte(character)
		scanner.position++
		scanner.prune(scanner.position)
	}
}

func (scanner *directProcessRedactionScanner) consumeByte(character byte) {
	switch scanner.state {
	case directProcessScanNormal:
		if directProcessASCIIAlpha(character) {
			scanner.state = directProcessScanScheme
		} else if character == '?' || character == '&' {
			scanner.resetQueryKey()
			scanner.state = directProcessScanQueryKey
		}
	case directProcessScanScheme:
		if directProcessSchemeCharacter(character) {
			// Validity is state-only: retaining an arbitrary scheme would make
			// a hostile single Write allocate without bound.
		} else if character == ':' {
			scanner.state = directProcessScanSlashOne
		} else {
			scanner.state = directProcessScanNormal
			scanner.consumeByte(character)
		}
	case directProcessScanSlashOne:
		if character == '/' {
			scanner.state = directProcessScanSlashTwo
		} else {
			scanner.state = directProcessScanNormal
			scanner.consumeByte(character)
		}
	case directProcessScanSlashTwo:
		if character == '/' {
			scanner.authorityStart = scanner.position + 1
			scanner.state = directProcessScanAuthority
		} else {
			scanner.state = directProcessScanNormal
			scanner.consumeByte(character)
		}
	case directProcessScanAuthority:
		scanner.consumeAuthorityQuery(character)
		if character == '@' {
			if scanner.position > scanner.authorityStart {
				scanner.addSpan(scanner.authorityStart, scanner.position)
			}
			scanner.resetAuthorityQuery()
			scanner.state = directProcessScanNormal
		} else if character == '/' || directProcessSpace(character) {
			scanner.leaveAuthority(character)
		}
	case directProcessScanQueryKey:
		if character == '=' {
			if directProcessSensitiveKey(string(scanner.queryKey)) {
				scanner.valueStart = scanner.position + 1
				scanner.state = directProcessScanSensitiveValue
			} else {
				scanner.state = directProcessScanNormal
			}
		} else if character == '?' || character == '&' {
			scanner.resetQueryKey()
		} else if directProcessQueryTerminator(character) {
			scanner.state = directProcessScanNormal
		} else if len(scanner.queryKey) < 64 {
			scanner.queryKey = append(scanner.queryKey, directProcessLowerASCII(character))
		} else {
			scanner.state = directProcessScanNormal
		}
	case directProcessScanSensitiveValue:
		if character == '&' || directProcessQueryTerminator(character) {
			scanner.addSpan(scanner.valueStart, scanner.position)
			scanner.state = directProcessScanNormal
			scanner.consumeByte(character)
		}
	}
}

func (scanner *directProcessRedactionScanner) resetQueryKey() {
	if cap(scanner.queryKey) < 64 {
		scanner.queryKey = make([]byte, 0, 64)
		return
	}
	scanner.queryKey = scanner.queryKey[:0]
}

func (scanner *directProcessRedactionScanner) resetAuthorityQueryKey() {
	if cap(scanner.authorityQueryKey) < 64 {
		scanner.authorityQueryKey = make([]byte, 0, 64)
		return
	}
	scanner.authorityQueryKey = scanner.authorityQueryKey[:0]
}

func (scanner *directProcessRedactionScanner) resetAuthorityQuery() {
	scanner.authorityQueryState = directProcessScanNormal
	scanner.authorityQueryValueStart = 0
	scanner.resetAuthorityQueryKey()
}

// consumeAuthorityQuery recognizes a named query independently from the
// still-possible userinfo. The visible userinfo grammar permits both '?' and
// '#', so neither can settle the authority until a later '@' or '/' does.
func (scanner *directProcessRedactionScanner) consumeAuthorityQuery(character byte) {
	switch scanner.authorityQueryState {
	case directProcessScanNormal:
		if character == '?' || character == '&' {
			scanner.resetAuthorityQueryKey()
			scanner.authorityQueryState = directProcessScanQueryKey
		}
	case directProcessScanQueryKey:
		if character == '=' {
			if directProcessSensitiveKey(string(scanner.authorityQueryKey)) {
				scanner.authorityQueryValueStart = scanner.position + 1
				scanner.authorityQueryState = directProcessScanSensitiveValue
			} else {
				scanner.authorityQueryState = directProcessScanNormal
			}
		} else if character == '?' || character == '&' {
			scanner.resetAuthorityQueryKey()
		} else if directProcessQueryTerminator(character) {
			scanner.authorityQueryState = directProcessScanNormal
		} else if len(scanner.authorityQueryKey) < 64 {
			scanner.authorityQueryKey = append(scanner.authorityQueryKey, directProcessLowerASCII(character))
		} else {
			scanner.authorityQueryState = directProcessScanNormal
		}
	case directProcessScanSensitiveValue:
		if character == '&' {
			scanner.addSpan(scanner.authorityQueryValueStart, scanner.position)
			scanner.resetAuthorityQueryKey()
			scanner.authorityQueryState = directProcessScanQueryKey
		} else if directProcessQueryTerminator(character) {
			scanner.addSpan(scanner.authorityQueryValueStart, scanner.position)
			scanner.authorityQueryState = directProcessScanNormal
		}
	}
}

func (scanner *directProcessRedactionScanner) leaveAuthority(character byte) {
	state := scanner.authorityQueryState
	switch state {
	case directProcessScanQueryKey:
		scanner.resetQueryKey()
		scanner.queryKey = append(scanner.queryKey, scanner.authorityQueryKey...)
	case directProcessScanSensitiveValue:
		scanner.valueStart = scanner.authorityQueryValueStart
	}
	scanner.resetAuthorityQuery()
	scanner.state = state
	if state == directProcessScanNormal {
		scanner.consumeByte(character)
	}
}

func (scanner *directProcessRedactionScanner) finish() {
	if scanner.state == directProcessScanAuthority && scanner.authorityQueryState == directProcessScanSensitiveValue {
		scanner.addSpan(scanner.authorityQueryValueStart, scanner.position)
		scanner.resetAuthorityQuery()
	}
	if scanner.state == directProcessScanSensitiveValue {
		scanner.addSpan(scanner.valueStart, scanner.position)
		scanner.state = directProcessScanNormal
	}
}

func (scanner *directProcessRedactionScanner) addSpan(start, end int64) {
	if end < start {
		return
	}
	span := directProcessRedactionSpan{start: start, end: end}
	if start < directProcessInspectionBytes && end > 0 {
		scanner.headSpans = append(scanner.headSpans, span)
	}
	scanner.compactTailSpans()
	scanner.tailSpans = append(scanner.tailSpans, span)
}

func (scanner *directProcessRedactionScanner) prune(total int64) {
	cutoff := total - directProcessInspectionBytes
	for scanner.tailSpanStart < len(scanner.tailSpans) && scanner.tailSpans[scanner.tailSpanStart].end <= cutoff {
		scanner.tailSpanStart++
	}
	scanner.compactTailSpans()
}

func (scanner *directProcessRedactionScanner) activeTailSpans() []directProcessRedactionSpan {
	return scanner.tailSpans[scanner.tailSpanStart:]
}

func (scanner *directProcessRedactionScanner) compactTailSpans() {
	if scanner.tailSpanStart == 0 {
		return
	}
	if scanner.tailSpanStart < len(scanner.tailSpans) && scanner.tailSpanStart*2 < len(scanner.tailSpans) {
		return
	}
	copy(scanner.tailSpans, scanner.tailSpans[scanner.tailSpanStart:])
	scanner.tailSpans = scanner.tailSpans[:len(scanner.tailSpans)-scanner.tailSpanStart]
	scanner.tailSpanStart = 0
}

func directProcessSensitiveKey(value string) bool {
	switch value {
	case "token", "access_token", "password", "passwd", "secret", "api_key", "apikey", "auth", "authorization":
		return true
	default:
		return false
	}
}

func directProcessASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}
func directProcessSchemeCharacter(value byte) bool {
	return directProcessASCIIAlpha(value) || value >= '0' && value <= '9' || value == '+' || value == '-' || value == '.'
}
func directProcessLowerASCII(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}
func directProcessSpace(value byte) bool {
	// Go regexp's Perl \s class is exactly [\t\n\f\r ] in byte mode.
	return value == ' ' || value == '\t' || value == '\n' || value == '\f' || value == '\r'
}
func directProcessQueryTerminator(value byte) bool {
	return value == '#' || value == '\'' || value == '"' || directProcessSpace(value)
}

func sanitizedDirectProcessEnvironment(environment []string) ([]string, error) {
	allowed := map[string]bool{"PATH": true, "SystemRoot": true, "WINDIR": true, "ComSpec": true, "TMP": true, "TEMP": true, "TMPDIR": true}
	wtree := map[string]bool{
		"WTREE_PROJECT_ID": true, "WTREE_WORKSPACE": true, "WTREE_REPOSITORY_ID": true, "WTREE_MOUNT": true,
		"WTREE_PATH": true, "WTREE_BRANCH": true, "WTREE_COMMIT": true,
	}
	values := make(map[string]string)
	for _, item := range environment {
		key, value, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		if strings.HasPrefix(key, "WTREE_") && !wtree[key] {
			return nil, fmt.Errorf("unsupported direct process environment key %q", key)
		}
		if !allowed[key] && !wtree[key] {
			continue
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("duplicate direct process environment key %q", key)
		}
		values[key] = value
	}
	result := make([]string, 0, len(values)+9)
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "ComSpec", "TMP", "TEMP", "TMPDIR"} {
		if value, exists := values[key]; exists {
			result = append(result, key+"="+value)
		}
	}
	result = append(result,
		"HOME=", "XDG_CONFIG_HOME=", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GIT_ATTR_NOSYSTEM=1", "LC_ALL=C", "LANG=C",
	)
	for _, key := range []string{"WTREE_PROJECT_ID", "WTREE_WORKSPACE", "WTREE_REPOSITORY_ID", "WTREE_MOUNT", "WTREE_PATH", "WTREE_BRANCH", "WTREE_COMMIT"} {
		if value, exists := values[key]; exists {
			result = append(result, key+"="+value)
		}
	}
	return result, nil
}
