package sessions

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"open-server/internal/target"
)

const schemaVersion = 1

var ErrNotFound = errors.New("saved session not found")

type Store struct {
	Path string
}

type Entry struct {
	Name    string
	Target  string
	Options Options
}

type Options struct {
	Port        *int    `yaml:"port,omitempty"`
	RSH         *string `yaml:"rsh,omitempty"`
	Duration    *string `yaml:"duration,omitempty"`
	Title       *string `yaml:"title,omitempty"`
	NoOpen      *bool   `yaml:"no-open,omitempty"`
	TensorBoard *bool   `yaml:"tensorboard,omitempty"`
	Python      *string `yaml:"python-interpreter,omitempty"`
	LaTeX       *bool   `yaml:"latex,omitempty"`
}

type savedFile struct {
	Version  int                     `yaml:"version"`
	Sessions map[string]savedSession `yaml:"sessions"`
}

type savedSession struct {
	Target  string   `yaml:"target"`
	Options *Options `yaml:"options,omitempty"`
}

func DefaultStore() (Store, error) {
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		return Store{}, fmt.Errorf("find user configuration directory: %w", err)
	}
	return Store{Path: filepath.Join(configDirectory, "open-server", "sessions", "saved-sessions.yaml")}, nil
}

func (s Store) Add(name, targetValue string, options Options) error {
	if err := validateName(name); err != nil {
		return err
	}
	if _, err := target.Parse(targetValue); err != nil {
		return fmt.Errorf("invalid target for saved session %q: %w", name, err)
	}
	if err := validateOptions(options); err != nil {
		return fmt.Errorf("invalid options for saved session %q: %w", name, err)
	}

	contents, err := s.load()
	if err != nil {
		return err
	}
	var savedOptions *Options
	if !options.empty() {
		savedOptions = &options
	}
	contents.Sessions[name] = savedSession{Target: targetValue, Options: savedOptions}
	return s.save(contents)
}

func (s Store) Delete(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	contents, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := contents.Sessions[name]; !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	delete(contents.Sessions, name)
	return s.save(contents)
}

func (s Store) Resolve(name string) (Entry, error) {
	if err := validateName(name); err != nil {
		return Entry{}, err
	}
	contents, err := s.load()
	if err != nil {
		return Entry{}, err
	}
	session, ok := contents.Sessions[name]
	if !ok {
		return Entry{}, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return Entry{Name: name, Target: session.Target, Options: session.options()}, nil
}

func (s Store) List() ([]Entry, error) {
	contents, err := s.load()
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(contents.Sessions))
	for name, session := range contents.Sessions {
		entries = append(entries, Entry{Name: name, Target: session.Target, Options: session.options()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func (s Store) load() (savedFile, error) {
	contents := savedFile{Version: schemaVersion, Sessions: make(map[string]savedSession)}
	file, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return contents, nil
	}
	if err != nil {
		return savedFile{}, fmt.Errorf("open saved sessions %q: %w", s.Path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&contents); err != nil {
		return savedFile{}, fmt.Errorf("read saved sessions %q: %w", s.Path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple YAML documents are not supported")
		}
		return savedFile{}, fmt.Errorf("read saved sessions %q: %w", s.Path, err)
	}
	if contents.Version != schemaVersion {
		return savedFile{}, fmt.Errorf("read saved sessions %q: unsupported version %d", s.Path, contents.Version)
	}
	if contents.Sessions == nil {
		contents.Sessions = make(map[string]savedSession)
	}
	for name, session := range contents.Sessions {
		if err := validateName(name); err != nil {
			return savedFile{}, fmt.Errorf("read saved sessions %q: %w", s.Path, err)
		}
		if _, err := target.Parse(session.Target); err != nil {
			return savedFile{}, fmt.Errorf("read saved sessions %q: invalid target for session %q: %w", s.Path, name, err)
		}
		if err := validateOptions(session.options()); err != nil {
			return savedFile{}, fmt.Errorf("read saved sessions %q: invalid options for session %q: %w", s.Path, name, err)
		}
	}
	return contents, nil
}

func (s savedSession) options() Options {
	if s.Options == nil {
		return Options{}
	}
	return *s.Options
}

func (o Options) empty() bool {
	return o.Port == nil && o.RSH == nil && o.Duration == nil && o.Title == nil && o.NoOpen == nil && o.TensorBoard == nil && o.Python == nil && o.LaTeX == nil
}

func validateOptions(options Options) error {
	if options.Port != nil && (*options.Port < 0 || *options.Port > 65535) {
		return errors.New("port must be between 0 and 65535")
	}
	if options.RSH != nil && *options.RSH == "" {
		return errors.New("rsh executable cannot be empty")
	}
	if options.Python != nil && *options.Python == "" {
		return errors.New("python interpreter cannot be empty")
	}
	if options.Duration != nil {
		duration, err := time.ParseDuration(*options.Duration)
		if err != nil || duration < 0 {
			return errors.New("duration must be a valid non-negative Go duration")
		}
	}
	return nil
}

func (s Store) save(contents savedFile) error {
	if s.Path == "" {
		return errors.New("saved sessions path cannot be empty")
	}
	directory := filepath.Dir(s.Path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create saved sessions directory %q: %w", directory, err)
	}

	temporary, err := os.CreateTemp(directory, ".saved-sessions-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary saved sessions file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary saved sessions file: %w", err)
	}
	encoder := yaml.NewEncoder(temporary)
	encoder.SetIndent(2)
	if err := encoder.Encode(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write saved sessions: %w", err)
	}
	if err := encoder.Close(); err != nil {
		temporary.Close()
		return fmt.Errorf("finish saved sessions: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync saved sessions: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close saved sessions: %w", err)
	}
	if err := os.Rename(temporaryPath, s.Path); err != nil {
		return fmt.Errorf("replace saved sessions %q: %w", s.Path, err)
	}
	return nil
}

func validateName(name string) error {
	if len(name) == 0 || len(name) > 64 {
		return errors.New("session name must be between 1 and 64 characters")
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return fmt.Errorf("invalid session name %q: use only letters, numbers, '.', '_', and '-'", name)
	}
	if name[0] == '-' {
		return errors.New("session name cannot start with '-'")
	}
	return nil
}
