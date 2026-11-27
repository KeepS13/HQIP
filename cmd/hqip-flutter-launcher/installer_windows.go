//go:build windows

package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/KeepS13/HQIP/internal/flutterrelease"
	"golang.org/x/sys/windows/registry"
)

const previewUninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\{87A84998-B48F-4B56-99C7-717315BFA38D}_is1`

type installedPreview struct {
	Directory string
	Version   string
	Sequence  int64
	Mode      string
	Message   string
}

type previewInstallRecord struct {
	Directory string
	Version   string
}

type previewInstallMetadata struct {
	Product  string `json:"product"`
	Channel  string `json:"channel"`
	Version  string `json:"version"`
	Sequence int64  `json:"sequence"`
}

var previewVersionText = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,80}$`)
var legacyPreviewVersion = regexp.MustCompile(`^0\.1\.0-preview\.([1-9][0-9]*)$`)

// This entry point is executed from the NEW installer's temporary directory,
// before IPC/mutex/startup. It never executes files from the installed directory.
func installerInspect(args []string) error {
	if len(args) != 2 {
		return errors.New("installer inspection requires a candidate directory and report path")
	}
	sequence, err := strconv.ParseInt(bundledSequence, 10, 64)
	if err != nil || sequence <= 0 {
		return errors.New("invalid installer sequence")
	}
	records, err := previewInstallRecords()
	var result installedPreview
	if err == nil {
		result, err = inspectPreviewInstallation(args[0], records, sequence, updateKey)
	}
	if err != nil {
		result.Mode = "blocked"
		result.Message = err.Error()
	}
	return writeInstallerReport(args[1], result)
}

func previewInstallRecords() ([]previewInstallRecord, error) {
	var records []previewInstallRecord
	// Older per-user Inno entries can be in either registry view. Only the
	// independent preview AppId is inspected; the formal client's key is separate.
	for _, hive := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
			key, err := registry.OpenKey(hive, previewUninstallKey, registry.QUERY_VALUE|view)
			if errors.Is(err, registry.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("无法读取现有预览版安装记录：%w", err)
			}
			directory, _, _ := key.GetStringValue("InstallLocation")
			if directory == "" {
				directory, _, _ = key.GetStringValue("Inno Setup: App Path")
			}
			version, _, _ := key.GetStringValue("DisplayVersion")
			key.Close()
			if directory == "" {
				return nil, errors.New("现有预览版安装记录缺少安装目录，请先修复该记录")
			}
			records = append(records, previewInstallRecord{directory, version})
		}
	}
	return records, nil
}

func inspectPreviewInstallation(candidate string, records []previewInstallRecord, incoming int64, public string) (installedPreview, error) {
	result := installedPreview{Directory: filepath.Clean(candidate), Mode: "install"}
	var registeredVersion string
	for i, record := range records {
		directory := filepath.Clean(record.Directory)
		if !filepath.IsAbs(directory) || strings.ContainsAny(directory, "\r\n\x00\"") {
			return result, errors.New("现有预览版安装目录无效，已停止安装")
		}
		if i > 0 && !strings.EqualFold(result.Directory, directory) {
			return result, errors.New("发现多个不同目录的预览版安装记录，请先通过系统应用设置清理重复记录")
		}
		result.Directory = directory
		if record.Version != "" {
			if registeredVersion != "" && record.Version != registeredVersion {
				return result, errors.New("预览版安装记录中的版本不一致，已停止安装")
			}
			registeredVersion = record.Version
		}
	}
	if !filepath.IsAbs(result.Directory) || strings.ContainsAny(result.Directory, "\r\n\x00\"") {
		return result, errors.New("请选择有效的绝对安装路径")
	}
	metadata, err := readInstallJSON(filepath.Join(result.Directory, "install.json"))
	if err == nil {
		var info previewInstallMetadata
		if json.Unmarshal(metadata, &info) != nil || info.Product != flutterrelease.Product || info.Channel != flutterrelease.Channel || info.Sequence <= 0 || !previewVersionText.MatchString(info.Version) {
			return result, errors.New("现有预览版的安装版本信息已损坏，已停止覆盖安装")
		}
		result.Version, result.Sequence = info.Version, info.Sequence
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("无法读取现有预览版的安装版本信息：%w", err)
	}
	if registeredVersion != "" && registeredVersion != result.Version {
		// Preview 1–3 installers predate install.json and stored only this value.
		match := legacyPreviewVersion.FindStringSubmatch(registeredVersion)
		if len(match) != 2 {
			return result, errors.New("无法识别现有预览版版本，已停止覆盖安装")
		}
		registeredSequence, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || registeredSequence <= 0 {
			return result, errors.New("现有预览版版本序号无效，已停止覆盖安装")
		}
		if registeredSequence > result.Sequence {
			result.Version, result.Sequence = registeredVersion, registeredSequence
		}
	}
	for _, name := range []string{"active.json", "pending.json"} {
		raw, err := readInstallJSON(filepath.Join(result.Directory, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, fmt.Errorf("无法读取现有更新状态 %s：%w", name, err)
		}
		var envelope flutterrelease.Envelope
		if json.Unmarshal(raw, &envelope) != nil {
			return result, fmt.Errorf("现有更新状态 %s 已损坏，已停止覆盖安装", name)
		}
		release, err := envelope.Verify(public)
		if err != nil || release.Platform != "windows-amd64" || release.Kind != "update" || !previewVersionText.MatchString(release.Version) {
			return result, fmt.Errorf("现有更新状态 %s 未通过签名或渠道校验，已停止覆盖安装", name)
		}
		// Keep even a staged/missing higher version protected from downgrade.
		// The normal launcher still verifies the complete payload before running it.
		if release.Sequence > result.Sequence {
			result.Version, result.Sequence = release.Version, release.Sequence
		}
	}
	if result.Sequence == 0 {
		_, launcherErr := os.Stat(filepath.Join(result.Directory, "HQIP Preview.exe"))
		_, runtimeErr := os.Stat(filepath.Join(result.Directory, "versions", "bundled", "hqip-runtime.exe"))
		if len(records) > 0 || !errors.Is(launcherErr, os.ErrNotExist) || !errors.Is(runtimeErr, os.ErrNotExist) {
			return result, errors.New("检测到已有预览版，但无法确认版本；为避免降级，已停止覆盖安装")
		}
		return result, nil
	}
	switch {
	case result.Sequence > incoming:
		result.Mode = "downgrade"
		result.Message = "已安装或已准备安装更高版本 " + result.Version + "，不能使用较旧安装包覆盖。请使用相同或更高序号的安装包。"
	case result.Sequence == incoming:
		result.Mode = "repair"
	default:
		result.Mode = "upgrade"
	}
	return result, nil
}

func readInstallJSON(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return nil, err
	}
	if len(raw) > 65536 {
		return nil, errors.New("版本文件超过大小限制")
	}
	return raw, nil
}

func writeInstallerReport(path string, result installedPreview) error {
	clean := strings.NewReplacer("\r", " ", "\n", " ", "\x00", " ")
	text := fmt.Sprintf("[Install]\r\nDirectory=%s\r\nVersion=%s\r\nSequence=%d\r\nMode=%s\r\nMessage=%s\r\n", clean.Replace(result.Directory), clean.Replace(result.Version), result.Sequence, result.Mode, clean.Replace(result.Message))
	// Windows' INI reader recognizes UTF-16 BOM; custom paths may be Chinese.
	units := utf16.Encode([]rune("\ufeff" + text))
	raw := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(raw[i*2:], unit)
	}
	return os.WriteFile(path, raw, 0600)
}
