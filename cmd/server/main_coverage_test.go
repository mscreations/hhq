package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
)

// --- attachmentLabel's remaining branches (main.go) ---
// (main_test.go doesn't test attachmentLabel at all yet.)

// TestAttachmentLabelReturnsURIVerbatimOnParseError covers the url.Parse
// error branch: a URI net/url can't parse at all (missing scheme here) must
// be returned unchanged rather than panicking or returning an empty label.
func TestAttachmentLabelReturnsURIVerbatimOnParseError(t *testing.T) {
	const bad = "://not-a-valid-uri"
	got := attachmentLabel(bad)
	if got != bad {
		t.Fatalf("attachmentLabel(%q) = %q, want the input returned unchanged", bad, got)
	}
}

// TestAttachmentLabelReturnsURIVerbatimWhenPathEmptyOrRoot covers the
// `base == "." || base == "/"` branch: a URI with no path (base ".") or a
// bare "/" path (base "/") isn't a usable filename, so the whole URI should
// be returned instead of a useless "." or "/" label.
func TestAttachmentLabelReturnsURIVerbatimWhenPathEmptyOrRoot(t *testing.T) {
	cases := []string{
		"https://example.com",  // empty path -> path.Base("") == "."
		"https://example.com/", // root path -> path.Base("/") == "/"
	}
	for _, uri := range cases {
		if got := attachmentLabel(uri); got != uri {
			t.Errorf("attachmentLabel(%q) = %q, want the input returned unchanged", uri, got)
		}
	}
}

// TestAttachmentLabelReturnsRawBaseOnPathUnescapeError covers the
// url.PathUnescape error branch. url.Parse itself already validates/decodes
// percent-escapes once while building u.Path, so to make the *second*
// PathUnescape call (on the already-decoded last path segment) fail, the
// segment must decode via Parse into something that looks like an invalid
// escape itself - e.g. the raw URI encodes a literal "%" as "%25", so Parse
// decodes "%25zz" once into "%zz", and re-running PathUnescape("%zz")
// fails since "zz" isn't valid hex.
func TestAttachmentLabelReturnsRawBaseOnPathUnescapeError(t *testing.T) {
	const uri = "https://example.com/%25zz"
	got := attachmentLabel(uri)
	if got != "%zz" {
		t.Fatalf("attachmentLabel(%q) = %q, want the raw (still-escaped) base %q", uri, got, "%zz")
	}
}

// --- templateDict's error branches (main.go) ---

func TestTemplateDictBuildsMapFromPairs(t *testing.T) {
	got, err := templateDict("a", 1, "b", "two")
	if err != nil {
		t.Fatalf("templateDict: %v", err)
	}
	if got["a"] != 1 || got["b"] != "two" {
		t.Fatalf("templateDict = %+v", got)
	}
}

func TestTemplateDictErrorsOnOddArgCount(t *testing.T) {
	_, err := templateDict("a", 1, "b")
	if err == nil {
		t.Fatal("expected an error for an odd number of arguments")
	}
}

func TestTemplateDictErrorsOnNonStringKey(t *testing.T) {
	_, err := templateDict(1, "value")
	if err == nil {
		t.Fatal("expected an error when a key isn't a string")
	}
}

// --- bootstrapFromFile (main.go) - currently 0% covered ---

type fakeBootstrapEntry struct {
	Name string `json:"name"`
}

func parseFakeBootstrapEntries(raw string) ([]fakeBootstrapEntry, error) {
	var entries []fakeBootstrapEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parsing fake bootstrap file: %w", err)
	}
	return entries, nil
}

// TestBootstrapFromFileNoOpsWhenFileMissing covers the `raw == ""` no-op
// path (config.ReadBootstrapFile returns "", nil for a missing file) -
// apply must never be called.
func TestBootstrapFromFileNoOpsWhenFileMissing(t *testing.T) {
	dir := t.TempDir()
	applyCalled := false

	bootstrapFromFile(context.Background(), dir, "does-not-exist.json", "fake entries",
		parseFakeBootstrapEntries,
		func(ctx context.Context, entries []fakeBootstrapEntry) { applyCalled = true })

	if applyCalled {
		t.Fatal("expected apply not to be called when the bootstrap file doesn't exist")
	}
}

// TestBootstrapFromFileNoOpsOnReadError covers config.ReadBootstrapFile
// returning a real error (distinct from the "file doesn't exist" no-op
// above, which config.ReadBootstrapFile itself special-cases via
// errors.Is(err, os.ErrNotExist) and turns into a "", nil no-op rather than
// an error at all). A NUL byte in the filename makes os.ReadFile fail with
// "invalid argument" - confirmed empirically to NOT satisfy
// errors.Is(err, os.ErrNotExist) (unlike, on Windows, joining a path onto a
// regular file rather than a directory, which surfaces as "the system cannot
// find the path specified" and Go's os package maps to fs.ErrNotExist same
// as a simple missing file - so that more "obvious" approach doesn't
// actually reach this branch on this platform).
func TestBootstrapFromFileNoOpsOnReadError(t *testing.T) {
	dir := t.TempDir()

	applyCalled := false
	bootstrapFromFile(context.Background(), dir, "bad\x00name.json", "fake entries",
		parseFakeBootstrapEntries,
		func(ctx context.Context, entries []fakeBootstrapEntry) { applyCalled = true })

	if applyCalled {
		t.Fatal("expected apply not to be called when reading the bootstrap file errors")
	}
}

// TestBootstrapFromFileNoOpsOnParseError covers the parse-error branch:
// malformed JSON must be logged and skipped rather than calling apply with
// a zero-value/partial result.
func TestBootstrapFromFileNoOpsOnParseError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	applyCalled := false
	bootstrapFromFile(context.Background(), dir, "bad.json", "fake entries",
		parseFakeBootstrapEntries,
		func(ctx context.Context, entries []fakeBootstrapEntry) { applyCalled = true })

	if applyCalled {
		t.Fatal("expected apply not to be called when parsing the bootstrap file fails")
	}
}

// TestBootstrapFromFileCallsApplyWithParsedEntriesAndContext covers the
// success path end to end: a well-formed file's parsed entries and the
// caller's own context must both reach apply.
func TestBootstrapFromFileCallsApplyWithParsedEntriesAndContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.json"), []byte(`[{"name":"Alice"},{"name":"Bob"}]`), 0600); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")

	var gotEntries []fakeBootstrapEntry
	var gotCtxValue any
	bootstrapFromFile(ctx, dir, "good.json", "fake entries",
		parseFakeBootstrapEntries,
		func(applyCtx context.Context, entries []fakeBootstrapEntry) {
			gotEntries = entries
			gotCtxValue = applyCtx.Value(ctxKey{})
		})

	if len(gotEntries) != 2 || gotEntries[0].Name != "Alice" || gotEntries[1].Name != "Bob" {
		t.Fatalf("apply received entries = %+v", gotEntries)
	}
	if gotCtxValue != "marker" {
		t.Fatalf("apply's context didn't carry the caller's value through, got %v", gotCtxValue)
	}
}

// --- bootstrapFirstParent's remaining error branches (main.go) ---

// TestBootstrapFirstParentListParentsError covers the ListParents-error
// branch: a UserStore backed by a connection that can never actually reach
// a server (bad DSN, in the same spirit as internal/db/db_test.go's
// malformed-DSN tests) makes the very first query fail.
func TestBootstrapFirstParentListParentsError(t *testing.T) {
	badConn, err := sql.Open("pgx", "postgres://user:pass@127.0.0.1:1/nonexistent?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer badConn.Close()

	users := &models.UserStore{DB: badConn}
	t.Setenv("BOOTSTRAP_PARENT_EMAIL", "bootstrap@example.com")
	t.Setenv("BOOTSTRAP_PARENT_PASSWORD", "some-password")

	if err := bootstrapFirstParent(context.Background(), users); err == nil {
		t.Fatal("expected bootstrapFirstParent to fail when ListParents can't reach the database")
	}
}

// TestBootstrapFirstParentHashPasswordError covers the auth.HashPassword
// error branch: bcrypt rejects passwords longer than 72 bytes
// (bcrypt.ErrPasswordTooLong), which is the only realistic way HashPassword
// fails.
func TestBootstrapFirstParentHashPasswordError(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &models.UserStore{DB: conn}

	t.Setenv("BOOTSTRAP_PARENT_EMAIL", "bootstrap-toolong@example.com")
	t.Setenv("BOOTSTRAP_PARENT_PASSWORD", strings.Repeat("x", 100)) // > 72 bytes

	if err := bootstrapFirstParent(context.Background(), users); err == nil {
		t.Fatal("expected bootstrapFirstParent to fail when HashPassword rejects an over-length password")
	}

	parents, err := users.ListParents(context.Background())
	if err != nil {
		t.Fatalf("ListParents: %v", err)
	}
	if len(parents) != 0 {
		t.Fatalf("expected no parent to be created when hashing fails, got %d", len(parents))
	}
}

// TestBootstrapFirstParentCreateParentError covers the CreateParent-error
// branch specifically (distinct from the ListParents/HashPassword branches
// above): ListParents must return an empty list (so bootstrap doesn't skip
// early) while CreateParent itself fails. This is arranged by inserting a
// *child* row (raw SQL, bypassing the model layer - CreateChild doesn't
// accept an email) with the same email bootstrap is about to use: children
// aren't included in ListParents's `WHERE role = 'parent'` filter, but
// hhq_users.email is UNIQUE across every role, so bootstrap's own
// INSERT ... RETURNING id still collides.
func TestBootstrapFirstParentCreateParentError(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &models.UserStore{DB: conn}

	const dupEmail = "colliding-child-email@example.com"
	if _, err := conn.ExecContext(context.Background(),
		`INSERT INTO hhq_users (name, role, email) VALUES ('Existing Child Row', 'child', $1)`, dupEmail); err != nil {
		t.Fatalf("inserting colliding child row: %v", err)
	}

	t.Setenv("BOOTSTRAP_PARENT_EMAIL", dupEmail)
	t.Setenv("BOOTSTRAP_PARENT_PASSWORD", "some-password")

	if err := bootstrapFirstParent(context.Background(), users); err == nil {
		t.Fatal("expected bootstrapFirstParent to fail when CreateParent's email collides with an existing row")
	}
}

// --- loadOrGenerateSecret's remaining error branches (main.go) ---

// TestLoadOrGenerateSecretSettingsGetError covers the settings.Get-error
// branch: a SettingsStore backed by an unreachable connection makes the
// very first lookup fail (distinct from TestLoadOrGenerateSecretReturnsEnvValueWithoutTouchingSettings
// and TestLoadOrGenerateSecretGeneratesAndPersistsWhenUnset in main_test.go,
// neither of which exercises this branch since both use a real, reachable
// database).
func TestLoadOrGenerateSecretSettingsGetError(t *testing.T) {
	badConn, err := sql.Open("pgx", "postgres://user:pass@127.0.0.1:1/nonexistent?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer badConn.Close()

	settings := &models.SettingsStore{DB: badConn}
	_, err = loadOrGenerateSecret(context.Background(), settings, testEncryptor(t), "", "some_key", "SOME_SECRET")
	if err == nil {
		t.Fatal("expected loadOrGenerateSecret to fail when settings.Get can't reach the database")
	}
	if !strings.Contains(err.Error(), "loading stored") {
		t.Fatalf("error = %v, want it to include the 'loading stored' wrap context", err)
	}
}

// TestLoadOrGenerateSecretDecodeStoredHexError covers the
// hex.DecodeString-error branch: a stored settings value that isn't valid
// hex (shouldn't happen via normal operation, since this code path is the
// only writer, but defends against manual DB tampering/corruption) must
// surface as a wrapped "decoding stored" error rather than panicking.
func TestLoadOrGenerateSecretDecodeStoredHexError(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}
	ctx := context.Background()

	if err := settings.Set(ctx, "bad_hex_key", "this-is-not-valid-hex!!"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	_, err := loadOrGenerateSecret(ctx, settings, testEncryptor(t), "", "bad_hex_key", "SOME_SECRET")
	if err == nil {
		t.Fatal("expected loadOrGenerateSecret to fail when the stored value isn't valid hex")
	}
	if !strings.Contains(err.Error(), "decoding stored") {
		t.Fatalf("error = %v, want it to include the 'decoding stored' wrap context", err)
	}
}

// TestLoadOrGenerateSecretDecryptStoredError covers the enc.Decrypt-error
// branch: a stored value that's valid hex but not a real ciphertext this
// Encryptor can open (e.g. corrupted, or encrypted with a different key)
// must surface as a wrapped "decrypting stored" error.
func TestLoadOrGenerateSecretDecryptStoredError(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}
	ctx := context.Background()

	garbage := hex.EncodeToString([]byte("not a real gcm ciphertext, just random-ish bytes padded out"))
	if err := settings.Set(ctx, "bad_ciphertext_key", garbage); err != nil {
		t.Fatalf("Set: %v", err)
	}

	_, err := loadOrGenerateSecret(ctx, settings, testEncryptor(t), "", "bad_ciphertext_key", "SOME_SECRET")
	if err == nil {
		t.Fatal("expected loadOrGenerateSecret to fail when the stored ciphertext can't be decrypted")
	}
	if !strings.Contains(err.Error(), "decrypting stored") {
		t.Fatalf("error = %v, want it to include the 'decrypting stored' wrap context", err)
	}
}

// NOTE on loadOrGenerateSecret's `if _, err := rand.Read(raw); err != nil`
// branch (main.go): as of Go 1.24+ (see https://go.dev/issue/66821),
// crypto/rand.Read's own doc comment states it "never returns an error, and
// always fills b entirely" - internally, if the underlying Reader ever
// errors, Read calls a linked runtime `fatal()` and crashes the process
// irrecoverably instead of returning an error. That was confirmed here by
// actually swapping rand.Reader for a failing implementation and observing
// the test binary crash with "crypto/rand: failed to read random data" -
// exactly the fatal() message from that stdlib source - rather than getting
// back a normal error to assert against. This means that specific
// `err != nil` check in loadOrGenerateSecret is genuinely unreachable in
// this Go version and can never be exercised by any test (there is no way
// to observe the error return without crashing the whole process, which
// would also crash `go test` itself). Left in place as defensive code (it's
// harmless, and older Go versions/non-standard platforms historically could
// return an error here), but documented as an intentionally-uncovered,
// dead-in-practice branch rather than silently skipped.

// TestLoadOrGenerateSecretEncryptError covers the enc.Encrypt-error branch,
// distinct from the rand.Read failure above: the 32-byte secret must be
// generated successfully (so execution reaches enc.Encrypt at all), and
// *that* call's own internal nonce generation must be what fails.
func TestLoadOrGenerateSecretEncryptError(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}

	realReader := rand.Reader
	fake := &afterFirstCallFailsReaderUsing{real: realReader}
	rand.Reader = fake
	defer func() { rand.Reader = realReader }()

	_, err := loadOrGenerateSecret(context.Background(), settings, testEncryptor(t), "", "encryptfail_key", "SOME_SECRET")
	if err == nil {
		t.Fatal("expected loadOrGenerateSecret to fail when Encrypt's nonce generation errors")
	}
	if !strings.Contains(err.Error(), "encrypting") {
		t.Fatalf("error = %v, want it to include the 'encrypting' wrap context", err)
	}
}

// afterFirstCallFailsReaderUsing succeeds on its first Read (delegating to a
// real io.Reader captured before the swap) and fails on every subsequent
// call - see TestLoadOrGenerateSecretEncryptError.
type afterFirstCallFailsReaderUsing struct {
	mu    sync.Mutex
	calls int
	real  io.Reader
}

func (r *afterFirstCallFailsReaderUsing) Read(p []byte) (int, error) {
	r.mu.Lock()
	r.calls++
	first := r.calls == 1
	r.mu.Unlock()
	if first {
		return io.ReadFull(r.real, p)
	}
	return 0, errors.New("simulated entropy source failure on 2nd+ call")
}

// --- loadOrGenerateSecret's settings.Set-error branch, via a minimal fake driver ---
//
// Forcing just the final INSERT/UPDATE (settings.Set) to fail while the
// preceding SELECT (settings.Get) succeeds with no stored row isn't
// reachable against a real Postgres instance without a multi-statement
// scripted failure, so (mirroring internal/caldav/caldav_fakedb_test.go's
// documented reasoning for the same class of problem) this registers a tiny
// fake database/sql driver that scripts the SELECT to return no rows and
// the INSERT to fail.

type fakeQueryFunc func(query string, args []driver.NamedValue) (driver.Rows, error)
type fakeExecFunc func(query string, args []driver.NamedValue) (driver.Result, error)

type fakeDBConfig struct {
	query fakeQueryFunc
	exec  fakeExecFunc
}

var fakeDriverConfigs = struct {
	mu   sync.Mutex
	byID map[string]*fakeDBConfig
}{byID: map[string]*fakeDBConfig{}}

type fakeSQLDriver struct{}

func (fakeSQLDriver) Open(name string) (driver.Conn, error) {
	fakeDriverConfigs.mu.Lock()
	cfg := fakeDriverConfigs.byID[name]
	fakeDriverConfigs.mu.Unlock()
	if cfg == nil {
		return nil, fmt.Errorf("fakeSQLDriver: no config registered for %q", name)
	}
	return &fakeConn{cfg: cfg}, nil
}

func init() {
	sql.Register("hhq-main-coverage-fake", fakeSQLDriver{})
}

func newFakeDB(t *testing.T, query fakeQueryFunc, exec fakeExecFunc) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())

	fakeDriverConfigs.mu.Lock()
	fakeDriverConfigs.byID[name] = &fakeDBConfig{query: query, exec: exec}
	fakeDriverConfigs.mu.Unlock()
	t.Cleanup(func() {
		fakeDriverConfigs.mu.Lock()
		delete(fakeDriverConfigs.byID, name)
		fakeDriverConfigs.mu.Unlock()
	})

	db, err := sql.Open("hhq-main-coverage-fake", name)
	if err != nil {
		t.Fatalf("sql.Open(hhq-main-coverage-fake): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type fakeConn struct {
	cfg *fakeDBConfig
}

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("fakeConn: Prepare not supported, expected QueryerContext/ExecerContext to be used instead")
}
func (c *fakeConn) Close() error { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fakeConn: transactions not supported")
}
func (c *fakeConn) CheckNamedValue(nv *driver.NamedValue) error { return nil }

func (c *fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.cfg.query == nil {
		return nil, fmt.Errorf("fakeConn: no query handler configured, got query: %s", query)
	}
	return c.cfg.query(query, args)
}

func (c *fakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.cfg.exec == nil {
		return nil, fmt.Errorf("fakeConn: no exec handler configured, got query: %s", query)
	}
	return c.cfg.exec(query, args)
}

var (
	_ driver.QueryerContext    = (*fakeConn)(nil)
	_ driver.ExecerContext     = (*fakeConn)(nil)
	_ driver.NamedValueChecker = (*fakeConn)(nil)
)

type fakeRows struct {
	cols []string
	data [][]driver.Value
	pos  int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.pos])
	r.pos++
	return nil
}

// TestLoadOrGenerateSecretSettingsSetError covers the settings.Set-error
// branch: the SELECT (settings.Get) returns no rows (as if the key had
// never been stored), so loadOrGenerateSecret proceeds to generate a new
// secret and persist it - and it's exactly that final Set/INSERT which the
// fake driver makes fail here.
func TestLoadOrGenerateSecretSettingsSetError(t *testing.T) {
	db := newFakeDB(t,
		func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "FROM hhq_settings") {
				return &fakeRows{cols: []string{"value"}}, nil // zero rows -> sql.ErrNoRows -> Get falls back to ""
			}
			return nil, fmt.Errorf("unexpected query: %s", query)
		},
		func(query string, args []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(query, "INSERT INTO hhq_settings") {
				return nil, errors.New("simulated settings-store failure")
			}
			return nil, fmt.Errorf("unexpected exec: %s", query)
		},
	)

	settings := &models.SettingsStore{DB: db}
	_, err := loadOrGenerateSecret(context.Background(), settings, testEncryptor(t), "", "some_key", "SOME_SECRET")
	if err == nil {
		t.Fatal("expected loadOrGenerateSecret to fail when settings.Set fails")
	}
	if !strings.Contains(err.Error(), "storing") {
		t.Fatalf("error = %v, want it to include the 'storing' wrap context", err)
	}
}
