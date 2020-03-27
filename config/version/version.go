// Package version is used to inject build time information via -X variables
package version

import (
	"fmt"
	"io/ioutil"
	"strings"

	"github.com/opencord/voltha-lib-go/v3/pkg/log"
)

// Default build-time variable.
// These values can (should) be overridden via ldflags when built with
// `make`
var (
	version   = "unknown-version"
	goVersion = "unknown-goversion"
	vcsRef    = "unknown-vcsref"
	vcsDirty  = "unknown-vcsdirty"
	buildTime = "unknown-buildtime"
	os        = "unknown-os"
	arch      = "unknown-arch"
)

// InfoType is a collection of build time environment variables
type InfoType struct {
	Version   string `json:"version"`
	GoVersion string `json:"goversion"`
	VcsRef    string `json:"vcsref"`
	VcsDirty  string `json:"vcsdirty"`
	BuildTime string `json:"buildtime"`
	Os        string `json:"os"`
	Arch      string `json:"arch"`
}

// VersionInfo is an instance of build time environment variables populated at build time via -X arguments
var VersionInfo InfoType

func init() {
	VersionInfo = InfoType{
		Version:   version,
		VcsRef:    vcsRef,
		VcsDirty:  vcsDirty,
		GoVersion: goVersion,
		Os:        os,
		Arch:      arch,
		BuildTime: buildTime,
	}
	_, _ = log.AddPackage(log.CONSOLE, log.DebugLevel, nil)
}

func (v InfoType) String(indent string) string {
	builder := strings.Builder{}

	builder.WriteString(fmt.Sprintf("%sVersion:      %s\n", indent, VersionInfo.Version))
	builder.WriteString(fmt.Sprintf("%sGoVersion:    %s\n", indent, VersionInfo.GoVersion))
	builder.WriteString(fmt.Sprintf("%sVCS Ref:      %s\n", indent, VersionInfo.VcsRef))
	builder.WriteString(fmt.Sprintf("%sVCS Dirty:    %s\n", indent, VersionInfo.VcsDirty))
	builder.WriteString(fmt.Sprintf("%sBuilt:        %s\n", indent, VersionInfo.BuildTime))
	builder.WriteString(fmt.Sprintf("%sOS/Arch:      %s/%s\n", indent, VersionInfo.Os, VersionInfo.Arch))
	return builder.String()
}

func GetCodeVersion() string {
	if VersionInfo.Version == "unknown-version" {
		content, err := ioutil.ReadFile("VERSION")
		if err == nil {
			return (string(content))
		} else {
			log.Error("VERSION-file not readable")
			return VersionInfo.Version
		}
	} else {
		return VersionInfo.Version
	}
}
