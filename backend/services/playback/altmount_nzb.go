package playback

import (
	"bytes"
	"encoding/xml"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"novastream/models"
)

var (
	altMountMediaExtensions = map[string]bool{
		".mkv": true, ".mp4": true, ".avi": true, ".ts": true, ".m4v": true, ".mov": true,
		".wmv": true, ".mpg": true, ".mpeg": true, ".xvid": true, ".rm": true, ".rmvb": true,
		".asf": true, ".asx": true, ".wtv": true, ".mk3d": true, ".dvr-ms": true,
	}
	altMountQuotedFilenamePattern = regexp.MustCompile(`"([^"]+)"`)
)

type altMountNZB struct {
	XMLName xml.Name          `xml:"nzb"`
	Head    altMountNZBHead   `xml:"head"`
	Files   []altMountNZBFile `xml:"file"`
}

type altMountNZBHead struct {
	Metas []altMountNZBMeta `xml:"meta"`
}

type altMountNZBMeta struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type altMountNZBFile struct {
	Poster   string              `xml:"poster,attr,omitempty"`
	Date     string              `xml:"date,attr,omitempty"`
	Subject  string              `xml:"subject,attr"`
	Groups   altMountNZBGroups   `xml:"groups"`
	Segments altMountNZBSegments `xml:"segments"`
}

type altMountNZBGroups struct {
	Groups []string `xml:"group"`
}

type altMountNZBSegments struct {
	Segments []altMountNZBSegment `xml:"segment"`
}

type altMountNZBSegment struct {
	Bytes  string `xml:"bytes,attr"`
	Number string `xml:"number,attr"`
	ID     string `xml:",chardata"`
}

func prepareAltMountNZBSubmission(candidate models.NZBResult, nzbBytes []byte, fileName string) ([]byte, string) {
	releaseName := altMountReleaseName(candidate, fileName)
	if releaseName == "" {
		return nzbBytes, fileName
	}

	var parsed altMountNZB
	if err := xml.Unmarshal(nzbBytes, &parsed); err != nil {
		return nzbBytes, fileName
	}

	ext := altMountInferMediaExtension(parsed, releaseName)
	if ext == "" || strings.HasSuffix(strings.ToLower(releaseName), ext) {
		return nzbBytes, fileName
	}

	releaseFileName := releaseName + ext
	changed := false
	for i := range parsed.Head.Metas {
		metaType := strings.ToLower(strings.TrimSpace(parsed.Head.Metas[i].Type))
		if metaType == "name" || metaType == "title" {
			parsed.Head.Metas[i].Value = releaseFileName
			changed = true
		}
	}

	bestIndex := -1
	bestBytes := int64(-1)
	for i, f := range parsed.Files {
		if altMountSubjectMediaExtension(f.Subject) == ext {
			if size := altMountSubjectBytes(f); size > bestBytes {
				bestIndex = i
				bestBytes = size
			}
		}
	}
	if bestIndex >= 0 {
		parsed.Files[bestIndex].Subject = altMountRewriteSubjectFilename(parsed.Files[bestIndex].Subject, releaseFileName)
		changed = true
	}
	if !changed {
		return nzbBytes, fileName
	}

	var out bytes.Buffer
	out.WriteString(xml.Header)
	out.WriteString(`<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">` + "\n")
	enc := xml.NewEncoder(&out)
	enc.Indent("", "  ")
	if err := enc.Encode(parsed); err != nil {
		return nzbBytes, fileName
	}
	if err := enc.Close(); err != nil {
		return nzbBytes, fileName
	}
	return out.Bytes(), ensureNZBExtension(releaseFileName)
}

func altMountReleaseName(candidate models.NZBResult, fileName string) string {
	for _, value := range []string{fileName, candidate.Title} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		base := path.Base(filepath.ToSlash(value))
		if strings.HasSuffix(strings.ToLower(base), ".nzb") {
			base = base[:len(base)-4]
		}
		if base != "" && base != "." && base != "/" {
			return base
		}
	}
	return ""
}

func altMountInferMediaExtension(nzb altMountNZB, releaseName string) string {
	for _, meta := range nzb.Head.Metas {
		if ext := altMountMediaExtension(meta.Value); ext != "" && !strings.EqualFold(strings.TrimSpace(meta.Value), releaseName) {
			return ext
		}
	}
	bestExt := ""
	bestBytes := int64(-1)
	for _, file := range nzb.Files {
		ext := altMountSubjectMediaExtension(file.Subject)
		if ext == "" {
			continue
		}
		if size := altMountSubjectBytes(file); size > bestBytes {
			bestExt = ext
			bestBytes = size
		}
	}
	return bestExt
}

func altMountSubjectMediaExtension(subject string) string {
	if match := altMountQuotedFilenamePattern.FindStringSubmatch(subject); len(match) == 2 {
		if ext := altMountMediaExtension(match[1]); ext != "" {
			return ext
		}
	}
	return altMountMediaExtension(subject)
}

func altMountMediaExtension(value string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(value)))
	if altMountMediaExtensions[ext] {
		return ext
	}
	return ""
}

func altMountSubjectBytes(file altMountNZBFile) int64 {
	var total int64
	for _, segment := range file.Segments.Segments {
		total += parsePositiveInt64(segment.Bytes)
	}
	return total
}

func altMountRewriteSubjectFilename(subject, fileName string) string {
	if altMountQuotedFilenamePattern.MatchString(subject) {
		return altMountQuotedFilenamePattern.ReplaceAllString(subject, `"`+fileName+`"`)
	}
	return subject + ` "` + fileName + `"`
}

func parsePositiveInt64(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}
