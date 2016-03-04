package exec

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"strings"

	"github.com/concourse/atc"
)

//go:generate counterfeiter . TaskConfigSource

// TaskConfigSource is used to determine a Task step's TaskConfig.
type TaskConfigSource interface {
	// FetchConfig returns the TaskConfig, and may have to a task config file out
	// of the SourceRepository.
	FetchConfig(*SourceRepository) (atc.TaskConfig, error)
	Warnings() []string
}

// StaticConfigSource represents a statically configured TaskConfig.
type StaticConfigSource struct {
	Plan atc.TaskPlan
}

// FetchConfig returns the configuration. It cannot fail.
func (configSource StaticConfigSource) FetchConfig(*SourceRepository) (atc.TaskConfig, error) {
	taskConfig := *configSource.Plan.Config
	if taskConfig.Params == nil {
		taskConfig.Params = map[string]string{}
	}
	for key, val := range configSource.Plan.Params {
		strVal, err := configSource.toString(val)
		if err != nil {
			return atc.TaskConfig{}, err
		}

		taskConfig.Params[key] = strVal
	}

	return taskConfig, nil
}

func (configSource StaticConfigSource) toString(obj interface{}) (string, error) {
	switch data := obj.(type) {
	case string:
		return data, nil
	default:
		str, err := json.Marshal(data)
		return string(str), err
	}
}

func (configSource StaticConfigSource) Warnings() []string {
	warnings := []string{}
	if configSource.Plan.ConfigPath != "" && configSource.Plan.Config != nil {
		warnings = append(warnings, "\x1b[31mDEPRECATION WARNING: Specifying both `file` and `config.params` in a task step is deprecated, use params on task step directly\x1b[0m")
	}

	return warnings
}

// DeprecationConfigSource represents a statically configured TaskConfig.
type DeprecationConfigSource struct {
	Delegate TaskConfigSource
	Stderr   io.Writer
}

// FetchConfig returns the configuration. It cannot fail.
func (configSource DeprecationConfigSource) FetchConfig(repo *SourceRepository) (atc.TaskConfig, error) {
	taskConfig, err := configSource.Delegate.FetchConfig(repo)
	if err != nil {
		return atc.TaskConfig{}, err
	}

	for _, warning := range configSource.Delegate.Warnings() {
		fmt.Fprintln(configSource.Stderr, warning)
	}

	return taskConfig, nil
}

func (configSource DeprecationConfigSource) Warnings() []string {
	return []string{}
}

// FileConfigSource represents a dynamically configured TaskConfig, which will
// be fetched from a specified file in the SourceRepository.
type FileConfigSource struct {
	Path string
}

// FetchConfig reads the specified file from the SourceRepository and loads the
// TaskConfig contained therein (expecting it to be YAML format).
//
// The path must be in the format SOURCE_NAME/FILE/PATH.yml. The SOURCE_NAME
// will be used to determine the ArtifactSource in the SourceRepository to
// stream the file out of.
//
// If the source name is missing (i.e. if the path is just "foo.yml"),
// UnspecifiedArtifactSourceError is returned.
//
// If the specified source name cannot be found, UnknownArtifactSourceError is
// returned.
//
// If the task config file is not found, or is invalid YAML, or is an invalid
// task configuration, the respective errors will be bubbled up.
func (configSource FileConfigSource) FetchConfig(repo *SourceRepository) (atc.TaskConfig, error) {
	segs := strings.SplitN(configSource.Path, "/", 2)
	if len(segs) != 2 {
		return atc.TaskConfig{}, UnspecifiedArtifactSourceError{configSource.Path}
	}

	sourceName := SourceName(segs[0])
	filePath := segs[1]

	source, found := repo.SourceFor(sourceName)
	if !found {
		return atc.TaskConfig{}, UnknownArtifactSourceError{sourceName}
	}

	stream, err := source.StreamFile(filePath)
	if err != nil {
		return atc.TaskConfig{}, err
	}

	defer stream.Close()

	streamedFile, err := ioutil.ReadAll(stream)
	if err != nil {
		return atc.TaskConfig{}, err
	}

	config, err := atc.LoadTaskConfig(streamedFile)
	if err != nil {
		return atc.TaskConfig{}, fmt.Errorf("failed to load %s: %s", configSource.Path, err)
	}

	return config, nil
}

func (configSource FileConfigSource) Warnings() []string {
	return []string{}
}

// MergedConfigSource is used to join two config sources together.
type MergedConfigSource struct {
	A TaskConfigSource
	B TaskConfigSource
}

// FetchConfig fetches both config sources, and merges the second config source
// into the first. This allows the user to set params required by a task loaded
// from a file by providing them in static configuration.
func (configSource MergedConfigSource) FetchConfig(source *SourceRepository) (atc.TaskConfig, error) {
	aConfig, err := configSource.A.FetchConfig(source)
	if err != nil {
		return atc.TaskConfig{}, err
	}

	bConfig, err := configSource.B.FetchConfig(source)
	if err != nil {
		return atc.TaskConfig{}, err
	}

	return aConfig.Merge(bConfig), nil
}

func (configSource MergedConfigSource) Warnings() []string {
	warnings := []string{}
	warnings = append(warnings, configSource.A.Warnings()...)
	warnings = append(warnings, configSource.B.Warnings()...)

	return warnings
}

// ValidatingConfigSource delegates to another ConfigSource, and validates its
// task config.
type ValidatingConfigSource struct {
	ConfigSource TaskConfigSource
}

// FetchConfig fetches the config using the underlying ConfigSource, and checks
// that it's valid.
func (configSource ValidatingConfigSource) FetchConfig(source *SourceRepository) (atc.TaskConfig, error) {
	config, err := configSource.ConfigSource.FetchConfig(source)
	if err != nil {
		return atc.TaskConfig{}, err
	}

	if err := config.Validate(); err != nil {
		return atc.TaskConfig{}, err
	}

	return config, nil
}

func (configSource ValidatingConfigSource) Warnings() []string {
	return configSource.ConfigSource.Warnings()
}

// UnknownArtifactSourceError is returned when the SourceName specified by the
// path does not exist in the SourceRepository.
type UnknownArtifactSourceError struct {
	SourceName SourceName
}

// Error returns a human-friendly error message.
func (err UnknownArtifactSourceError) Error() string {
	return fmt.Sprintf("unknown artifact source: %s", err.SourceName)
}

// UnspecifiedArtifactSourceError is returned when the specified path is of a
// file in the toplevel directory, and so it does not indicate a SourceName.
type UnspecifiedArtifactSourceError struct {
	Path string
}

// Error returns a human-friendly error message.
func (err UnspecifiedArtifactSourceError) Error() string {
	return fmt.Sprintf("config path '%s' does not specify where the file lives", err.Path)
}
