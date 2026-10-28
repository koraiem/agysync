package sync

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// ConsolidateOptions configures how local & synced conversations are harvested and reconciled
type ConsolidateOptions struct {
	DryRun           bool
	Verbosity        int
	PreferProjectIDs map[string]bool
}

// ConsolidateStats tracks counts of harvested, localized, and joined items
type ConsolidateStats struct {
	HarvestedIdeConversations int
	HarvestedCliConversations int
	HarvestedIdeBrains        int
	HarvestedCliBrains        int
	HarvestedAnnotations      int
	ProjectsCreated           int
	ProjectsConverged         int
	ConversationsJoined       int
	ConversationsLocalized    int
	SummariesReconciled       int
	CreatedProjectNames       []string
}

// CopySqliteDbSafe copies a SQLite .db file safely. If a companion -wal file exists,
// it uses `sqlite3 <src> ".backup '<dst>'"` to capture uncommitted WAL frames cleanly
// without deleting or corrupting live WAL journals.
func CopySqliteDbSafe(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	if _, err := os.Stat(src + "-wal"); err == nil {
		if _, lookErr := exec.LookPath("sqlite3"); lookErr == nil {
			escapedDst := strings.ReplaceAll(dst, "'", "''")
			_ = os.Remove(dst)
			cmd := exec.Command("sqlite3", src, fmt.Sprintf(".timeout 5000\n.backup '%s'", escapedDst))
			if err := cmd.Run(); err == nil {
				_ = os.Chmod(dst, 0644)
				return nil
			}
		}
	}

	if err := CopyFileOverwrite(src, dst); err != nil {
		return err
	}
	return os.Chmod(dst, 0644)
}

func queryDbStepCount(dbPath string) int64 {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return 0
	}
	cmd := exec.Command("sqlite3", dbPath, "SELECT COUNT(*) FROM steps;")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return v
}

// UpdateWorkspaceMetadataProto updates Field 1 (workspace_uri) and Field 2 (git_root_uri)
// inside a CortexWorkspaceMetadata protobuf message while preserving all other fields (GitInfo, branch).
func UpdateWorkspaceMetadataProto(wsBytes []byte, localizedURI string) ([]byte, bool) {
	if localizedURI == "" {
		return wsBytes, false
	}

	var out []byte
	changed := false
	hasField1 := false
	hasField2 := false
	remaining := wsBytes

	for len(remaining) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(remaining)
		if tagLen < 0 {
			return wsBytes, false
		}
		valLen := protowire.ConsumeFieldValue(num, typ, remaining[tagLen:])
		if valLen < 0 {
			return wsBytes, false
		}
		fieldRaw := remaining[:tagLen+valLen]
		valPortion := remaining[tagLen : tagLen+valLen]
		remaining = remaining[tagLen+valLen:]

		if typ == protowire.BytesType && (num == 1 || num == 2) {
			sval, _ := protowire.ConsumeString(valPortion)
			if num == 1 {
				hasField1 = true
				out = protowire.AppendTag(out, 1, protowire.BytesType)
				out = protowire.AppendString(out, localizedURI)
				if sval != localizedURI {
					changed = true
				}
				continue
			}
			if num == 2 {
				hasField2 = true
				out = protowire.AppendTag(out, 2, protowire.BytesType)
				out = protowire.AppendString(out, localizedURI)
				if sval != localizedURI {
					changed = true
				}
				continue
			}
		}

		out = append(out, fieldRaw...)
	}

	if !hasField1 {
		var prefix []byte
		prefix = protowire.AppendTag(prefix, 1, protowire.BytesType)
		prefix = protowire.AppendString(prefix, localizedURI)
		if !hasField2 {
			prefix = protowire.AppendTag(prefix, 2, protowire.BytesType)
			prefix = protowire.AppendString(prefix, localizedURI)
		}
		out = append(prefix, out...)
		changed = true
	}

	return out, changed
}

// UpdateTrajectoryMetadataProto updates localized workspace URIs (Fields 1 and 7) and project_id (Field 18)
// inside a CortexTrajectoryMetadata protobuf blob while preserving all other fields (e.g. Field 3, Field 15) byte-for-byte.
func UpdateTrajectoryMetadataProto(blob []byte, localizedURI, projectID string) ([]byte, bool) {
	var out []byte
	changed := false
	hasField1 := false
	hasField7 := false
	hasField18 := false
	remaining := blob

	for len(remaining) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(remaining)
		if tagLen < 0 {
			return blob, false
		}
		valLen := protowire.ConsumeFieldValue(num, typ, remaining[tagLen:])
		if valLen < 0 {
			return blob, false
		}
		fieldRaw := remaining[:tagLen+valLen]
		valPortion := remaining[tagLen : tagLen+valLen]
		remaining = remaining[tagLen+valLen:]

		if typ == protowire.BytesType {
			switch num {
			case 1:
				hasField1 = true
				if localizedURI != "" {
					subBytes, _ := protowire.ConsumeBytes(valPortion)
					newSub, subChanged := UpdateWorkspaceMetadataProto(subBytes, localizedURI)
					out = protowire.AppendTag(out, 1, protowire.BytesType)
					out = protowire.AppendBytes(out, newSub)
					if subChanged {
						changed = true
					}
					continue
				}
			case 7:
				hasField7 = true
				if localizedURI != "" {
					oldStr, _ := protowire.ConsumeString(valPortion)
					out = protowire.AppendTag(out, 7, protowire.BytesType)
					out = protowire.AppendString(out, localizedURI)
					if oldStr != localizedURI {
						changed = true
					}
					continue
				}
			case 18:
				hasField18 = true
				if projectID != "" {
					oldProj, _ := protowire.ConsumeString(valPortion)
					out = protowire.AppendTag(out, 18, protowire.BytesType)
					out = protowire.AppendString(out, projectID)
					if oldProj != projectID {
						changed = true
					}
					continue
				}
			}
		}

		out = append(out, fieldRaw...)
	}

	if localizedURI != "" && !hasField1 {
		wsSub, _ := UpdateWorkspaceMetadataProto(nil, localizedURI)
		var prefix []byte
		prefix = protowire.AppendTag(prefix, 1, protowire.BytesType)
		prefix = protowire.AppendBytes(prefix, wsSub)
		out = append(prefix, out...)
		changed = true
	}
	if localizedURI != "" && !hasField7 {
		out = protowire.AppendTag(out, 7, protowire.BytesType)
		out = protowire.AppendString(out, localizedURI)
		changed = true
	}
	if projectID != "" && !hasField18 {
		out = protowire.AppendTag(out, 18, protowire.BytesType)
		out = protowire.AppendString(out, projectID)
		changed = true
	}

	return out, changed
}

// ParseCascadeSummaryProto decodes a CascadeTrajectorySummary protobuf blob into IdeConversationInfo
func ParseCascadeSummaryProto(cascadeID string, raw []byte) *IdeConversationInfo {
	info := &IdeConversationInfo{
		CascadeID:      cascadeID,
		TrajectoryID:   cascadeID,
		Status:         1,
		RawSummaryBlob: raw,
	}

	remaining := raw
	for len(remaining) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(remaining)
		if tagLen < 0 {
			break
		}
		valLen := protowire.ConsumeFieldValue(num, typ, remaining[tagLen:])
		if valLen < 0 {
			break
		}
		valPortion := remaining[tagLen : tagLen+valLen]
		remaining = remaining[tagLen+valLen:]

		switch typ {
		case protowire.VarintType:
			v, _ := protowire.ConsumeVarint(valPortion)
			switch num {
			case 2:
				info.StepCount = int64(v)
			case 5:
				info.Status = int64(v)
			case 16:
				info.LastUserInputStep = int64(v)
			case 20:
				info.Source = int64(v)
			}
		case protowire.BytesType:
			bval, _ := protowire.ConsumeBytes(valPortion)
			switch num {
			case 1:
				info.Title = string(bval)
			case 3:
				sec, nano := parseTimestampProto(bval)
				if sec > 0 {
					info.LastModSec = sec
					info.LastModNano = nano
				}
			case 4:
				if len(bval) > 0 {
					info.TrajectoryID = string(bval)
				}
			case 7:
				sec, nano := parseTimestampProto(bval)
				if sec > 0 {
					info.CreatedSec = sec
					info.CreatedNano = nano
				}
			case 9:
				parseWorkspaceSubProto(bval, info)
			case 10:
				sec, nano := parseTimestampProto(bval)
				if sec > 0 {
					info.LastUserInputSec = sec
					info.LastUserInputNano = nano
				}
			case 15:
				parseAnnotationsSubProto(bval, info)
			case 17:
				info.RawMetadataBlob = bval
				parseTrajectoryMetadataBlob(bval, info)
			}
		}
	}

	return info
}

func parseTimestampProto(b []byte) (int64, int64) {
	var sec, nano int64
	rem := b
	for len(rem) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			break
		}
		valLen := protowire.ConsumeFieldValue(num, typ, rem[tagLen:])
		if valLen < 0 {
			break
		}
		valPortion := rem[tagLen : tagLen+valLen]
		rem = rem[tagLen+valLen:]
		if typ == protowire.VarintType {
			v, _ := protowire.ConsumeVarint(valPortion)
			if num == 1 {
				sec = int64(v)
			} else if num == 2 {
				nano = int64(v)
			}
		}
	}
	return sec, nano
}

func parseWorkspaceSubProto(b []byte, info *IdeConversationInfo) {
	rem := b
	for len(rem) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			break
		}
		valLen := protowire.ConsumeFieldValue(num, typ, rem[tagLen:])
		if valLen < 0 {
			break
		}
		valPortion := rem[tagLen : tagLen+valLen]
		rem = rem[tagLen+valLen:]
		if typ == protowire.BytesType {
			bval, _ := protowire.ConsumeBytes(valPortion)
			switch num {
			case 1:
				if info.WorkspaceURI == "" {
					info.WorkspaceURI = string(bval)
				}
			case 3:
				grem := bval
				for len(grem) > 0 {
					gnum, gtyp, glen := protowire.ConsumeTag(grem)
					if glen < 0 {
						break
					}
					gvlen := protowire.ConsumeFieldValue(gnum, gtyp, grem[glen:])
					if gvlen < 0 {
						break
					}
					gvp := grem[glen : glen+gvlen]
					grem = grem[glen+gvlen:]
					if gtyp == protowire.BytesType {
						gs, _ := protowire.ConsumeString(gvp)
						if gnum == 1 && info.GitRepo == "" {
							info.GitRepo = gs
						} else if gnum == 2 && info.GitURL == "" {
							info.GitURL = gs
						}
					}
				}
			case 4:
				if info.GitBranch == "" {
					info.GitBranch = string(bval)
				}
			}
		}
	}
}

func parseAnnotationsSubProto(b []byte, info *IdeConversationInfo) {
	rem := b
	for len(rem) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			break
		}
		valLen := protowire.ConsumeFieldValue(num, typ, rem[tagLen:])
		if valLen < 0 {
			break
		}
		valPortion := rem[tagLen : tagLen+valLen]
		rem = rem[tagLen+valLen:]
		if typ == protowire.BytesType {
			bval, _ := protowire.ConsumeBytes(valPortion)
			if num == 1 && info.AnnotationTitle == "" {
				info.AnnotationTitle = string(bval)
			} else if num == 7 {
				sec, nano := parseTimestampProto(bval)
				if sec > 0 {
					info.LastViewSec = sec
					info.LastViewNano = nano
				}
			}
		}
	}
}

// UpdateCascadeSummaryProto updates a CascadeTrajectorySummary protobuf blob in-place
// (preserving all unknown wire fields) with localized workspace URI, resolved project_id,
// step count, preview title, and annotations.
func UpdateCascadeSummaryProto(raw []byte, info *IdeConversationInfo) ([]byte, bool) {
	if len(raw) == 0 {
		return BuildCascadeTrajectorySummaryProto(info), true
	}

	var out []byte
	changed := false
	hasField1 := false
	hasField2 := false
	hasField9 := false
	hasField15 := false
	hasField17 := false

	remaining := raw
	for len(remaining) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(remaining)
		if tagLen < 0 {
			return BuildCascadeTrajectorySummaryProto(info), true
		}
		valLen := protowire.ConsumeFieldValue(num, typ, remaining[tagLen:])
		if valLen < 0 {
			return BuildCascadeTrajectorySummaryProto(info), true
		}
		fieldRaw := remaining[:tagLen+valLen]
		valPortion := remaining[tagLen : tagLen+valLen]
		remaining = remaining[tagLen+valLen:]

		if num == 1 && typ == protowire.BytesType {
			hasField1 = true
			oldTitle, _ := protowire.ConsumeString(valPortion)
			newTitle := oldTitle
			if (newTitle == "" || strings.HasPrefix(newTitle, "Conversation ")) && info.Title != "" {
				newTitle = info.Title
			}
			out = protowire.AppendTag(out, 1, protowire.BytesType)
			out = protowire.AppendString(out, newTitle)
			if newTitle != oldTitle {
				changed = true
			}
			continue
		}

		if num == 2 && typ == protowire.VarintType {
			hasField2 = true
			oldSteps, _ := protowire.ConsumeVarint(valPortion)
			newSteps := oldSteps
			if info.StepCount > int64(oldSteps) {
				newSteps = uint64(info.StepCount)
			}
			out = protowire.AppendTag(out, 2, protowire.VarintType)
			out = protowire.AppendVarint(out, newSteps)
			if newSteps != oldSteps {
				changed = true
			}
			continue
		}

		if num == 9 && typ == protowire.BytesType {
			hasField9 = true
			if info.WorkspaceURI != "" {
				subBytes, _ := protowire.ConsumeBytes(valPortion)
				newSub, subChanged := UpdateWorkspaceMetadataProto(subBytes, info.WorkspaceURI)
				out = protowire.AppendTag(out, 9, protowire.BytesType)
				out = protowire.AppendBytes(out, newSub)
				if subChanged {
					changed = true
				}
				continue
			}
		}

		if num == 15 && typ == protowire.BytesType {
			hasField15 = true
			subBytes, _ := protowire.ConsumeBytes(valPortion)
			newAnn, annChanged := updateAnnotationsSubProto(subBytes, info.AnnotationTitle, info.LastViewSec, info.LastViewNano)
			out = protowire.AppendTag(out, 15, protowire.BytesType)
			out = protowire.AppendBytes(out, newAnn)
			if annChanged {
				changed = true
			}
			continue
		}

		if num == 17 && typ == protowire.BytesType {
			hasField17 = true
			subBytes, _ := protowire.ConsumeBytes(valPortion)
			newMeta, metaChanged := UpdateTrajectoryMetadataProto(subBytes, info.WorkspaceURI, info.ProjectID)
			out = protowire.AppendTag(out, 17, protowire.BytesType)
			out = protowire.AppendBytes(out, newMeta)
			if metaChanged {
				changed = true
			}
			continue
		}

		out = append(out, fieldRaw...)
	}

	if !hasField1 && info.Title != "" {
		var prefix []byte
		prefix = protowire.AppendTag(prefix, 1, protowire.BytesType)
		prefix = protowire.AppendString(prefix, info.Title)
		out = append(prefix, out...)
		changed = true
	}
	if !hasField2 && info.StepCount > 0 {
		out = protowire.AppendTag(out, 2, protowire.VarintType)
		out = protowire.AppendVarint(out, uint64(info.StepCount))
		changed = true
	}
	if !hasField9 && info.WorkspaceURI != "" {
		wsSub, _ := UpdateWorkspaceMetadataProto(nil, info.WorkspaceURI)
		out = protowire.AppendTag(out, 9, protowire.BytesType)
		out = protowire.AppendBytes(out, wsSub)
		changed = true
	}
	if !hasField15 && (info.AnnotationTitle != "" || info.LastViewSec > 0) {
		annSub, _ := updateAnnotationsSubProto(nil, info.AnnotationTitle, info.LastViewSec, info.LastViewNano)
		out = protowire.AppendTag(out, 15, protowire.BytesType)
		out = protowire.AppendBytes(out, annSub)
		changed = true
	}
	if !hasField17 {
		baseMeta := info.RawMetadataBlob
		newMeta, _ := UpdateTrajectoryMetadataProto(baseMeta, info.WorkspaceURI, info.ProjectID)
		out = protowire.AppendTag(out, 17, protowire.BytesType)
		out = protowire.AppendBytes(out, newMeta)
		changed = true
	}

	return out, changed
}

func updateAnnotationsSubProto(annBytes []byte, title string, viewSec, viewNano int64) ([]byte, bool) {
	if title == "" && viewSec == 0 {
		return annBytes, false
	}
	var out []byte
	changed := false
	hasTitle := false
	hasView := false
	rem := annBytes

	for len(rem) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			break
		}
		valLen := protowire.ConsumeFieldValue(num, typ, rem[tagLen:])
		if valLen < 0 {
			break
		}
		fieldRaw := rem[:tagLen+valLen]
		valPortion := rem[tagLen : tagLen+valLen]
		rem = rem[tagLen+valLen:]

		if num == 1 && typ == protowire.BytesType && title != "" {
			hasTitle = true
			oldT, _ := protowire.ConsumeString(valPortion)
			out = protowire.AppendTag(out, 1, protowire.BytesType)
			out = protowire.AppendString(out, title)
			if oldT != title {
				changed = true
			}
			continue
		}
		if num == 7 && typ == protowire.BytesType && viewSec > 0 {
			hasView = true
			bval, _ := protowire.ConsumeBytes(valPortion)
			oldSec, _ := parseTimestampProto(bval)
			if viewSec > oldSec {
				var ts []byte
				ts = protowire.AppendTag(ts, 1, protowire.VarintType)
				ts = protowire.AppendVarint(ts, uint64(viewSec))
				if viewNano > 0 {
					ts = protowire.AppendTag(ts, 2, protowire.VarintType)
					ts = protowire.AppendVarint(ts, uint64(viewNano))
				}
				out = protowire.AppendTag(out, 7, protowire.BytesType)
				out = protowire.AppendBytes(out, ts)
				changed = true
				continue
			}
		}
		out = append(out, fieldRaw...)
	}

	if title != "" && !hasTitle {
		var prefix []byte
		prefix = protowire.AppendTag(prefix, 1, protowire.BytesType)
		prefix = protowire.AppendString(prefix, title)
		out = append(prefix, out...)
		changed = true
	}
	if viewSec > 0 && !hasView {
		var ts []byte
		ts = protowire.AppendTag(ts, 1, protowire.VarintType)
		ts = protowire.AppendVarint(ts, uint64(viewSec))
		if viewNano > 0 {
			ts = protowire.AppendTag(ts, 2, protowire.VarintType)
			ts = protowire.AppendVarint(ts, uint64(viewNano))
		}
		out = protowire.AppendTag(out, 7, protowire.BytesType)
		out = protowire.AppendBytes(out, ts)
		changed = true
	}

	return out, changed
}

// DecodeSummaryProtoMap decodes agyhub_summaries_proto.pb bytes into a map of cascade_id -> raw CascadeTrajectorySummary bytes
func DecodeSummaryProtoMap(data []byte) map[string][]byte {
	res := make(map[string][]byte)
	rem := data
	for len(rem) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			break
		}
		valLen := protowire.ConsumeFieldValue(num, typ, rem[tagLen:])
		if valLen < 0 {
			break
		}
		valPortion := rem[tagLen : tagLen+valLen]
		rem = rem[tagLen+valLen:]

		if num == 1 && typ == protowire.BytesType {
			entryBytes, _ := protowire.ConsumeBytes(valPortion)
			var key string
			var val []byte
			erem := entryBytes
			for len(erem) > 0 {
				enum, etyp, elen := protowire.ConsumeTag(erem)
				if elen < 0 {
					break
				}
				evlen := protowire.ConsumeFieldValue(enum, etyp, erem[elen:])
				if evlen < 0 {
					break
				}
				evp := erem[elen : elen+evlen]
				erem = erem[elen+evlen:]
				if etyp == protowire.BytesType {
					b, _ := protowire.ConsumeBytes(evp)
					if enum == 1 {
						key = string(b)
					} else if enum == 2 {
						val = b
					}
				}
			}
			if key != "" && len(val) > 0 {
				res[key] = val
			}
		}
	}
	return res
}

// EncodeSummaryProtoMap serializes a map of cascade_id -> raw CascadeTrajectorySummary bytes into agyhub_summaries_proto.pb format
func EncodeSummaryProtoMap(entries map[string][]byte) []byte {
	var keys []string
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []byte
	for _, k := range keys {
		v := entries[k]
		if len(v) == 0 {
			continue
		}
		var entry []byte
		entry = protowire.AppendTag(entry, 1, protowire.BytesType)
		entry = protowire.AppendString(entry, k)
		entry = protowire.AppendTag(entry, 2, protowire.BytesType)
		entry = protowire.AppendBytes(entry, v)

		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, entry)
	}
	return out
}

// MergeSummaryProtoFiles merges a remote agyhub_summaries_proto.pb file into the local one
func MergeSummaryProtoFiles(remoteFile, localFile string) error {
	remoteData, err := os.ReadFile(remoteFile)
	if err != nil {
		return err
	}
	localData, _ := os.ReadFile(localFile)

	remoteMap := DecodeSummaryProtoMap(remoteData)
	localMap := DecodeSummaryProtoMap(localData)

	for id, rBytes := range remoteMap {
		lBytes, exists := localMap[id]
		if !exists {
			localMap[id] = rBytes
			continue
		}
		rInfo := ParseCascadeSummaryProto(id, rBytes)
		lInfo := ParseCascadeSummaryProto(id, lBytes)

		// Choose the entry with more steps (or newer timestamp on tie)
		if rInfo.StepCount > lInfo.StepCount || (rInfo.StepCount == lInfo.StepCount && rInfo.LastModSec > lInfo.LastModSec) {
			if rInfo.AnnotationTitle == "" && lInfo.AnnotationTitle != "" {
				rInfo.AnnotationTitle = lInfo.AnnotationTitle
			}
			if rInfo.ProjectID == "" && lInfo.ProjectID != "" {
				rInfo.ProjectID = lInfo.ProjectID
			}
			merged, _ := UpdateCascadeSummaryProto(rBytes, rInfo)
			localMap[id] = merged
		} else {
			if lInfo.AnnotationTitle == "" && rInfo.AnnotationTitle != "" {
				lInfo.AnnotationTitle = rInfo.AnnotationTitle
			}
			if lInfo.ProjectID == "" && rInfo.ProjectID != "" {
				lInfo.ProjectID = rInfo.ProjectID
			}
			merged, _ := UpdateCascadeSummaryProto(lBytes, lInfo)
			localMap[id] = merged
		}
	}

	if err := os.MkdirAll(filepath.Dir(localFile), 0755); err != nil {
		return err
	}
	return os.WriteFile(localFile, EncodeSummaryProtoMap(localMap), 0644)
}

// CanonicalizeSummaryProtoFile writes a canonicalized copy (with file:///~/... URIs) of agyhub_summaries_proto.pb
func CanonicalizeSummaryProtoFile(srcFile, dstFile string, paths *Paths) error {
	data, err := os.ReadFile(srcFile)
	if err != nil {
		return err
	}
	localHome := InferHomeFromPaths(paths)
	entries := DecodeSummaryProtoMap(data)
	for id, raw := range entries {
		uris := FindRemoteURIs(raw)
		curr := raw
		for _, uri := range uris {
			canURI := CanonicalizeWorkspaceURI(uri, localHome)
			if canURI != "" && canURI != uri {
				if updated, rep, err := TranslateProtobuf(curr, uri, canURI); err == nil && rep {
					curr = updated
				}
			}
		}
		entries[id] = curr
	}
	if err := os.MkdirAll(filepath.Dir(dstFile), 0755); err != nil {
		return err
	}
	return os.WriteFile(dstFile, EncodeSummaryProtoMap(entries), 0644)
}

var (
	rePbtxtTitle   = regexp.MustCompile(`title:\s*"((?:[^"\\]|\\.)*)"`)
	rePbtxtSeconds = regexp.MustCompile(`seconds:\s*(\d+)`)
	rePbtxtNanos   = regexp.MustCompile(`nanos:\s*(\d+)`)
)

func parseAnnotationPbtxt(filePath string) (string, int64, int64) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", 0, 0
	}
	text := string(data)
	var title string
	if m := rePbtxtTitle.FindStringSubmatch(text); len(m) > 1 {
		title = strings.ReplaceAll(m[1], `\"`, `"`)
		title = strings.ReplaceAll(title, `\\`, `\`)
	}
	var sec, nano int64
	if m := rePbtxtSeconds.FindStringSubmatch(text); len(m) > 1 {
		sec, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if m := rePbtxtNanos.FindStringSubmatch(text); len(m) > 1 {
		nano, _ = strconv.ParseInt(m[1], 10, 64)
	}
	return title, sec, nano
}

// ConsolidateAndReconcile harvests orphaned IDE and CLI conversations, brains, and annotations
// into the unified ~/.gemini/antigravity/ directory, localizes cross-machine workspace paths,
// creates/converges ~/.gemini/config/projects/*.json definitions, joins orphaned conversations
// into their corresponding projects, and updates all 4 summary/trajectory stores in lockstep.
func ConsolidateAndReconcile(paths *Paths, opts ConsolidateOptions) (*ConsolidateStats, error) {
	stats := &ConsolidateStats{}
	if paths == nil {
		return stats, nil
	}

	// 1. Load & converge project registry
	reg, err := LoadProjectRegistry(paths, opts.PreferProjectIDs, opts.DryRun)
	if err != nil && opts.Verbosity >= 1 {
		fmt.Printf("Warning: error loading project registry: %v\n", err)
	}
	stats.ProjectsConverged = len(reg.AliasToNewID)

	// 2. Harvest conversations (.db and .pb) from antigravity-ide and antigravity-cli into antigravity
	cliOriginated := make(map[string]bool)
	harvestedInDryRun := make(map[string]string) // cascadeID -> source file path

	harvestConversationsFrom := func(srcDir string, isCli bool) {
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.IsDir() || ShouldIgnoreSyncPath(entry.Name()) {
				continue
			}
			name := entry.Name()
			isDb := strings.HasSuffix(name, ".db")
			isPb := strings.HasSuffix(name, ".pb")
			if !isDb && !isPb {
				continue
			}
			cascadeID := strings.TrimSuffix(strings.TrimSuffix(name, ".db"), ".pb")
			if isCli {
				cliOriginated[cascadeID] = true
			}

			srcPath := filepath.Join(srcDir, name)
			dstPath := filepath.Join(paths.CoreConversations, name)

			shouldHarvest := false
			dstInfo, dstErr := os.Stat(dstPath)
			if os.IsNotExist(dstErr) {
				// Also check if a .db already exists in Core when considering a .pb
				if isPb {
					if _, dbErr := os.Stat(filepath.Join(paths.CoreConversations, cascadeID+".db")); dbErr == nil {
						continue
					}
				}
				shouldHarvest = true
			} else if dstErr == nil {
				if isDb {
					srcSteps := queryDbStepCount(srcPath)
					dstSteps := queryDbStepCount(dstPath)
					if srcSteps > dstSteps {
						shouldHarvest = true
					}
				} else if isPb {
					if srcInfo, err := os.Stat(srcPath); err == nil && srcInfo.Size() > dstInfo.Size() {
						shouldHarvest = true
					}
				}
			}

			if shouldHarvest {
				if isCli {
					stats.HarvestedCliConversations++
				} else {
					stats.HarvestedIdeConversations++
				}
				harvestedInDryRun[cascadeID] = srcPath
				if !opts.DryRun {
					if isDb {
						_ = CopySqliteDbSafe(srcPath, dstPath)
					} else {
						_ = CopyFileOverwrite(srcPath, dstPath)
					}
				}
			}
		}
	}

	harvestConversationsFrom(paths.IdeConversations, false)
	harvestConversationsFrom(paths.CliConversations, true)

	// 3. Harvest brain directories from antigravity-ide/brain and antigravity-cli/brain into antigravity/brain
	harvestBrainsFrom := func(srcBrainDir string, isCli bool) {
		entries, err := os.ReadDir(srcBrainDir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() || ShouldIgnoreSyncPath(entry.Name()) {
				continue
			}
			srcSub := filepath.Join(srcBrainDir, entry.Name())
			dstSub := filepath.Join(paths.CoreBrain, entry.Name())
			missing, err := getFilesMissingInDst(srcSub, dstSub)
			if err == nil && len(missing) > 0 {
				if isCli {
					stats.HarvestedCliBrains++
				} else {
					stats.HarvestedIdeBrains++
				}
				if !opts.DryRun {
					_ = MergeDirectories(srcSub, dstSub, &SyncStats{}, opts.Verbosity, paths)
				}
			}
		}
	}

	harvestBrainsFrom(paths.IdeBrain, false)
	harvestBrainsFrom(paths.CliBrain, true)

	// 4. Harvest annotations from antigravity-cli/annotations into antigravity/annotations
	if entries, err := os.ReadDir(paths.CliAnnotations); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pbtxt") {
				continue
			}
			srcAnn := filepath.Join(paths.CliAnnotations, entry.Name())
			dstAnn := filepath.Join(paths.CoreAnnotations, entry.Name())
			if _, err := os.Stat(dstAnn); os.IsNotExist(err) {
				stats.HarvestedAnnotations++
				if !opts.DryRun {
					_ = CopyFile(srcAnn, dstAnn)
				}
			}
		}
	}

	// 5. Gather existing summary metadata across all stores (proto map, SQLite summaries, legacy state.vscdb, annotations)
	summaryByID := make(map[string]*IdeConversationInfo)

	mergeSummaryInfo := func(cand *IdeConversationInfo) {
		if cand == nil || cand.CascadeID == "" {
			return
		}
		existing, ok := summaryByID[cand.CascadeID]
		if !ok {
			summaryByID[cand.CascadeID] = cand
			return
		}
		if cand.StepCount > existing.StepCount {
			existing.StepCount = cand.StepCount
		}
		if cand.LastModSec > existing.LastModSec {
			existing.LastModSec = cand.LastModSec
			existing.LastModNano = cand.LastModNano
		}
		if existing.CreatedSec == 0 && cand.CreatedSec > 0 {
			existing.CreatedSec = cand.CreatedSec
			existing.CreatedNano = cand.CreatedNano
		}
		if (existing.Title == "" || strings.HasPrefix(existing.Title, "Conversation ")) && cand.Title != "" {
			existing.Title = cand.Title
		}
		if existing.AnnotationTitle == "" && cand.AnnotationTitle != "" {
			existing.AnnotationTitle = cand.AnnotationTitle
		}
		if existing.WorkspaceURI == "" && cand.WorkspaceURI != "" {
			existing.WorkspaceURI = cand.WorkspaceURI
		}
		if existing.ProjectID == "" && cand.ProjectID != "" {
			existing.ProjectID = cand.ProjectID
		}
		if existing.Source == 0 && cand.Source > 0 {
			existing.Source = cand.Source
		}
		if len(existing.RawSummaryBlob) == 0 && len(cand.RawSummaryBlob) > 0 {
			existing.RawSummaryBlob = cand.RawSummaryBlob
		}
		if len(existing.RawMetadataBlob) == 0 && len(cand.RawMetadataBlob) > 0 {
			existing.RawMetadataBlob = cand.RawMetadataBlob
		}
	}

	// 5a. From agyhub_summaries_proto.pb
	if data, err := os.ReadFile(paths.CoreSummariesPb); err == nil {
		for id, raw := range DecodeSummaryProtoMap(data) {
			mergeSummaryInfo(ParseCascadeSummaryProto(id, raw))
		}
	}

	// 5b. From antigravity/conversation_summaries.db and antigravity-cli/conversation_summaries.db
	loadSummariesFromSqlite(paths.CoreSummariesDb, false, mergeSummaryInfo)
	loadSummariesFromSqlite(paths.CliSummariesDb, true, mergeSummaryInfo)

	// 5c. From legacy IDE state.vscdb (antigravityUnifiedStateSync.trajectorySummaries)
	for _, vscdb := range paths.IdeStateVscdbFiles {
		loadSummariesFromVscdb(vscdb, mergeSummaryInfo)
	}

	// 6. Discover all conversations on disk in CoreConversations (plus DryRun pending harvests)
	diskDbPath := make(map[string]string)
	diskPbPath := make(map[string]string)

	if entries, err := os.ReadDir(paths.CoreConversations); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || ShouldIgnoreSyncPath(entry.Name()) {
				continue
			}
			name := entry.Name()
			if strings.HasSuffix(name, ".db") {
				id := strings.TrimSuffix(name, ".db")
				diskDbPath[id] = filepath.Join(paths.CoreConversations, name)
			} else if strings.HasSuffix(name, ".pb") {
				id := strings.TrimSuffix(name, ".pb")
				diskPbPath[id] = filepath.Join(paths.CoreConversations, name)
			}
		}
	}
	for id, srcPath := range harvestedInDryRun {
		if strings.HasSuffix(srcPath, ".db") {
			if _, ok := diskDbPath[id]; !ok {
				diskDbPath[id] = srcPath
			}
		} else if strings.HasSuffix(srcPath, ".pb") {
			if _, ok := diskPbPath[id]; !ok {
				diskPbPath[id] = srcPath
			}
		}
	}

	allIDsMap := make(map[string]bool)
	for id := range summaryByID {
		allIDsMap[id] = true
	}
	for id := range diskDbPath {
		allIDsMap[id] = true
	}
	for id := range diskPbPath {
		allIDsMap[id] = true
	}

	var allIDs []string
	for id := range allIDsMap {
		allIDs = append(allIDs, id)
	}
	sort.Strings(allIDs)

	localProjects := LoadLocalProjectPaths(paths)
	localHome := InferHomeFromPaths(paths)

	finalSummaries := make(map[string]*IdeConversationInfo)
	finalProtoMap := make(map[string][]byte)

	for _, id := range allIDs {
		info := summaryByID[id]
		var dbInfo *IdeConversationInfo
		dbFile := diskDbPath[id]
		if dbFile != "" {
			parsed, err := ParseConversationDb(dbFile, paths)
			if err == nil && parsed != nil {
				dbInfo = parsed
			}
		}

		if info == nil && dbInfo != nil {
			info = dbInfo
		} else if info != nil && dbInfo != nil {
			// Merge DB ground-truth into summary info
			if dbInfo.StepCount > info.StepCount {
				info.StepCount = dbInfo.StepCount
			}
			if info.WorkspaceURI == "" && dbInfo.WorkspaceURI != "" {
				info.WorkspaceURI = dbInfo.WorkspaceURI
			}
			if info.ProjectID == "" && dbInfo.ProjectID != "" {
				info.ProjectID = dbInfo.ProjectID
			}
			if (info.Title == "" || strings.HasPrefix(info.Title, "Conversation ")) && dbInfo.Title != "" {
				info.Title = dbInfo.Title
			}
			if len(dbInfo.RawMetadataBlob) > 0 {
				info.RawMetadataBlob = dbInfo.RawMetadataBlob
			}
		} else if info == nil {
			// .pb file with no existing summary entry
			pbFile := diskPbPath[id]
			var modSec int64 = time.Now().Unix()
			if fi, err := os.Stat(pbFile); err == nil {
				modSec = fi.ModTime().Unix()
			}
			title := extractConversationTitle(id, "", paths)
			info = &IdeConversationInfo{
				CascadeID:    id,
				TrajectoryID: id,
				Title:        title,
				StepCount:    1,
				Status:       1,
				CreatedSec:   modSec,
				LastModSec:   modSec,
			}
		}

		// Apply explicit annotation from annotations/<id>.pbtxt if available
		for _, annDir := range []string{paths.CoreAnnotations, paths.CliAnnotations} {
			if annDir == "" {
				continue
			}
			annTitle, viewSec, viewNano := parseAnnotationPbtxt(filepath.Join(annDir, id+".pbtxt"))
			if annTitle != "" && info.AnnotationTitle == "" {
				info.AnnotationTitle = annTitle
			}
			if viewSec > info.LastViewSec {
				info.LastViewSec = viewSec
				info.LastViewNano = viewNano
			}
		}

		if info.Title == "" && info.AnnotationTitle != "" {
			info.Title = info.AnnotationTitle
		}
		if info.Title == "" {
			info.Title = fmt.Sprintf("Conversation %s", id[:minInt(8, len(id))])
		}

		// Localize WorkspaceURI
		oldWorkspaceURI := info.WorkspaceURI
		if dbInfo != nil && dbInfo.RawWorkspaceURI != "" && oldWorkspaceURI == dbInfo.WorkspaceURI {
			oldWorkspaceURI = dbInfo.RawWorkspaceURI
		}
		var localizedPath string
		if info.WorkspaceURI != "" {
			localizedPath = LocalizeWorkspacePath(info.WorkspaceURI, localProjects, localHome)
			if localizedPath != "" {
				info.WorkspaceURI = NormalizeWorkspaceURI(localizedPath)
			}
		}
		if info.WorkspaceURI != oldWorkspaceURI {
			stats.ConversationsLocalized++
		}

		// Resolve or create ProjectID
		oldProjectID := info.ProjectID
		isCli := info.Source == 17 || cliOriginated[id]
		if isCli && info.Source == 0 {
			info.Source = 17
		}
		resolvedProjID, _ := reg.ResolveOrCreateProjectForWorkspace(localizedPath, oldProjectID, isCli, paths, opts.DryRun)
		info.ProjectID = resolvedProjID

		if resolvedProjID != oldProjectID && resolvedProjID != OutsideOfProjectID && resolvedProjID != DefaultCliProjectID {
			stats.ConversationsJoined++
		}

		// Update .db trajectory_metadata_blob if needed
		if dbFile != "" && !opts.DryRun {
			targetDbFile := filepath.Join(paths.CoreConversations, id+".db")
			if _, err := os.Stat(targetDbFile); err == nil {
				_ = updateConversationDbMetadata(targetDbFile, dbInfo, info.WorkspaceURI, info.ProjectID)
			}
		}

		// Build updated CascadeTrajectorySummary protobuf
		updatedSummaryBytes, _ := UpdateCascadeSummaryProto(info.RawSummaryBlob, info)
		info.RawSummaryBlob = updatedSummaryBytes
		finalSummaries[id] = info
		finalProtoMap[id] = updatedSummaryBytes
		stats.SummariesReconciled++
	}

	stats.ProjectsCreated = len(reg.CreatedIDs)
	for _, cid := range reg.CreatedIDs {
		if cfg := reg.ByID[cid]; cfg != nil {
			stats.CreatedProjectNames = append(stats.CreatedProjectNames, fmt.Sprintf("%s (%s)", cfg.Name, cfg.PrimaryFolderURI()))
		}
	}

	if opts.DryRun {
		return stats, nil
	}

	// 7. Write unified agyhub_summaries_proto.pb
	if paths.CoreSummariesPb != "" && len(finalProtoMap) > 0 {
		if err := os.MkdirAll(filepath.Dir(paths.CoreSummariesPb), 0755); err == nil {
			_ = os.WriteFile(paths.CoreSummariesPb, EncodeSummaryProtoMap(finalProtoMap), 0644)
		}
	}

	// 8. Reconcile unified conversation_summaries.db (and conversations_fts header rows)
	if paths.CoreSummariesDb != "" && len(finalSummaries) > 0 {
		if err := reconcileConversationSummariesSqlite(paths.CoreSummariesDb, finalSummaries); err != nil && opts.Verbosity >= 1 {
			fmt.Printf("Warning: error reconciling conversation_summaries.db: %v\n", err)
		}
	}

	// 9. Update projectsOrder in Desktop app_storage.json and IDE state.vscdb
	_ = UpdateProjectsOrder(paths, reg, opts.DryRun)

	// 10. Update legacy IDE state.vscdb trajectorySummaries if present
	_, _ = SyncIdeTrajectorySummaries(paths, opts.Verbosity)

	return stats, nil
}

func updateConversationDbMetadata(dbPath string, dbInfo *IdeConversationInfo, workspaceURI, projectID string) error {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}
	var currentBlob []byte
	if dbInfo != nil && len(dbInfo.RawMetadataBlob) > 0 {
		currentBlob = dbInfo.RawMetadataBlob
	} else {
		cmd := exec.Command("sqlite3", dbPath, "SELECT hex(data) FROM trajectory_metadata_blob WHERE id = 'main';")
		out, err := cmd.Output()
		if err != nil {
			return err
		}
		hexStr := strings.TrimSpace(string(out))
		if hexStr != "" {
			currentBlob, _ = hex.DecodeString(hexStr)
		}
	}

	updatedBlob, changed := UpdateTrajectoryMetadataProto(currentBlob, workspaceURI, projectID)
	if !changed {
		return nil
	}
	newHex := hex.EncodeToString(updatedBlob)
	sql := fmt.Sprintf("PRAGMA busy_timeout = 5000; INSERT INTO trajectory_metadata_blob(id, data) VALUES('main', x'%s') ON CONFLICT(id) DO UPDATE SET data = x'%s';", newHex, newHex)
	return exec.Command("sqlite3", dbPath, sql).Run()
}

func loadSummariesFromSqlite(dbPath string, isCli bool, onSummary func(*IdeConversationInfo)) {
	if dbPath == "" {
		return
	}
	if _, err := os.Stat(dbPath); err != nil {
		return
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return
	}

	query := "SELECT conversation_id, hex(raw_summary), project_id, replace(replace(title, char(10), ' '), char(13), ' '), replace(replace(preview, char(10), ' '), char(13), ' '), step_count, workspace_uris, source FROM conversation_summaries;"
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 8 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		rawHex := strings.TrimSpace(parts[1])
		projID := strings.TrimSpace(parts[2])
		annTitle := strings.TrimSpace(parts[3])
		preview := strings.TrimSpace(parts[4])
		stepCount, _ := strconv.ParseInt(strings.TrimSpace(parts[5]), 10, 64)
		wsJSON := strings.TrimSpace(parts[6])
		srcVal, _ := strconv.ParseInt(strings.TrimSpace(parts[7]), 10, 64)

		var info *IdeConversationInfo
		if rawHex != "" {
			if rawBytes, err := hex.DecodeString(rawHex); err == nil && len(rawBytes) > 0 {
				info = ParseCascadeSummaryProto(id, rawBytes)
			}
		}
		if info == nil {
			info = &IdeConversationInfo{
				CascadeID:    id,
				TrajectoryID: id,
				Status:       1,
			}
		}
		if projID != "" && info.ProjectID == "" {
			info.ProjectID = projID
		}
		if annTitle != "" && info.AnnotationTitle == "" {
			info.AnnotationTitle = annTitle
		}
		if preview != "" && info.Title == "" {
			info.Title = preview
		}
		if stepCount > info.StepCount {
			info.StepCount = stepCount
		}
		if info.WorkspaceURI == "" && wsJSON != "" {
			var uris []string
			if err := json.Unmarshal([]byte(wsJSON), &uris); err == nil && len(uris) > 0 {
				info.WorkspaceURI = uris[0]
			} else if strings.HasPrefix(wsJSON, "file://") {
				info.WorkspaceURI = wsJSON
			}
		}
		if isCli && info.Source == 0 {
			info.Source = 17
		} else if srcVal > 0 && info.Source == 0 {
			info.Source = srcVal
		}
		onSummary(info)
	}
}

func loadSummariesFromVscdb(vscdbPath string, onSummary func(*IdeConversationInfo)) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return
	}
	cmd := exec.Command("sqlite3", vscdbPath, "SELECT value FROM ItemTable WHERE key = 'antigravityUnifiedStateSync.trajectorySummaries' LIMIT 1;")
	out, err := cmd.Output()
	if err != nil {
		return
	}
	b64Val := strings.TrimSpace(string(out))
	if b64Val == "" {
		return
	}
	rawTable, err := base64.StdEncoding.DecodeString(b64Val)
	if err != nil {
		return
	}
	for id, b64Entry := range DecodeUssTable(rawTable) {
		if entryBytes, err := base64.StdEncoding.DecodeString(b64Entry); err == nil && len(entryBytes) > 0 {
			onSummary(ParseCascadeSummaryProto(id, entryBytes))
		}
	}
}

func reconcileConversationSummariesSqlite(dbPath string, summaries map[string]*IdeConversationInfo) error {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return err
	}

	var sb strings.Builder
	sb.WriteString("PRAGMA busy_timeout = 5000;\n")
	sb.WriteString("BEGIN IMMEDIATE;\n")
	sb.WriteString(`CREATE TABLE IF NOT EXISTS conversation_summaries (
    conversation_id TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    preview TEXT NOT NULL DEFAULT '',
    step_count INTEGER NOT NULL DEFAULT 0,
    last_modified_time TEXT NOT NULL DEFAULT '',
    workspace_uris TEXT NOT NULL DEFAULT '',
    status INTEGER NOT NULL DEFAULT 0,
    source INTEGER NOT NULL DEFAULT 0,
    project_id TEXT NOT NULL DEFAULT '',
    agent_name TEXT NOT NULL DEFAULT '',
    parent_conversation_id TEXT NOT NULL DEFAULT '',
    nesting_depth INTEGER NOT NULL DEFAULT 0,
    battle_id TEXT NOT NULL DEFAULT '',
    winning_conversation_id TEXT NOT NULL DEFAULT '',
    not_fully_idle INTEGER NOT NULL DEFAULT 0,
    killed INTEGER NOT NULL DEFAULT 0,
    last_user_input_time TEXT NOT NULL DEFAULT '',
    last_user_input_step_index INTEGER NOT NULL DEFAULT 0,
    app_data_dir TEXT NOT NULL DEFAULT '',
    raw_summary BLOB,
    group_id TEXT NOT NULL DEFAULT ''
);
`)
	sb.WriteString(`CREATE VIRTUAL TABLE IF NOT EXISTS conversations_fts USING fts5(
    conversation_id UNINDEXED,
    chunk_index UNINDEXED,
    step_Range_start UNINDEXED,
    step_range_end UNINDEXED,
    title,
    workspace,
    project,
    content,
    metadata
);
`)

	var ids []string
	for id := range summaries {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		info := summaries[id]
		escID := escapeSQLString(id)
		escTitle := escapeSQLString(info.AnnotationTitle)
		escPreview := escapeSQLString(info.Title)
		escProj := escapeSQLString(info.ProjectID)

		var wsJSON string
		if info.WorkspaceURI != "" {
			b, _ := json.Marshal([]string{info.WorkspaceURI})
			wsJSON = string(b)
		}
		escWS := escapeSQLString(wsJSON)

		modSec := info.LastModSec
		if modSec <= 0 {
			modSec = time.Now().Unix()
		}
		modTimeStr := time.Unix(modSec, info.LastModNano).UTC().Format(time.RFC3339Nano)
		escModTime := escapeSQLString(modTimeStr)

		userSec := info.LastUserInputSec
		if userSec <= 0 {
			userSec = info.CreatedSec
		}
		if userSec <= 0 {
			userSec = modSec
		}
		userTimeStr := time.Unix(userSec, info.LastUserInputNano).UTC().Format(time.RFC3339Nano)
		escUserTime := escapeSQLString(userTimeStr)

		status := info.Status
		if status <= 0 {
			status = 1
		}

		rawHex := hex.EncodeToString(info.RawSummaryBlob)

		sb.WriteString(fmt.Sprintf(
			`INSERT INTO conversation_summaries (
				conversation_id, title, preview, step_count, last_modified_time,
				workspace_uris, status, source, project_id, last_user_input_time,
				last_user_input_step_index, raw_summary
			) VALUES (
				'%s', '%s', '%s', %d, '%s',
				'%s', %d, %d, '%s', '%s',
				%d, x'%s'
			) ON CONFLICT(conversation_id) DO UPDATE SET
				title = CASE WHEN '%s' != '' THEN '%s' ELSE conversation_summaries.title END,
				preview = CASE WHEN '%s' != '' THEN '%s' ELSE conversation_summaries.preview END,
				step_count = CASE WHEN %d > conversation_summaries.step_count THEN %d ELSE conversation_summaries.step_count END,
				last_modified_time = CASE WHEN conversation_summaries.last_modified_time = '' THEN '%s' ELSE conversation_summaries.last_modified_time END,
				workspace_uris = CASE WHEN '%s' != '' THEN '%s' ELSE conversation_summaries.workspace_uris END,
				project_id = '%s',
				raw_summary = x'%s';
`,
			escID, escTitle, escPreview, info.StepCount, escModTime,
			escWS, status, info.Source, escProj, escUserTime,
			info.LastUserInputStep, rawHex,
			escTitle, escTitle,
			escPreview, escPreview,
			info.StepCount, info.StepCount,
			escModTime,
			escWS, escWS,
			escProj,
			rawHex,
		))

		ftsTitle := info.AnnotationTitle
		if ftsTitle == "" {
			ftsTitle = info.Title
		}
		escFtsTitle := escapeSQLString(ftsTitle)
		sb.WriteString(fmt.Sprintf("DELETE FROM conversations_fts WHERE conversation_id = '%s' AND chunk_index = -1;\n", escID))
		sb.WriteString(fmt.Sprintf(
			"INSERT INTO conversations_fts(conversation_id, chunk_index, step_Range_start, step_range_end, title, workspace, project, content, metadata) VALUES ('%s', -1, 0, 0, '%s', '%s', '%s', '', '');\n",
			escID, escFtsTitle, escWS, escProj,
		))
	}

	sb.WriteString("COMMIT;\n")

	cmd := exec.Command("sqlite3", dbPath)
	cmd.Stdin = strings.NewReader(sb.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// PrintConsolidateReport prints a summary of orphaned conversations, brains, and projects consolidated
func PrintConsolidateReport(stats *ConsolidateStats, dryRun bool) {
	if stats == nil {
		return
	}
	prefix := "Consolidated"
	joinVerb := "Joined"
	locVerb := "Localized"
	createVerb := "Created"
	convVerb := "Converged"
	if dryRun {
		prefix = "Would consolidate"
		joinVerb = "Would join"
		locVerb = "Would localize"
		createVerb = "Would create"
		convVerb = "Would converge"
	}

	fmt.Println("\n=== Unified History & Project Consolidation ===")
	fmt.Printf("  • %s %d IDE conversation(s) and %d CLI conversation(s) into antigravity/conversations\n",
		prefix, stats.HarvestedIdeConversations, stats.HarvestedCliConversations)
	fmt.Printf("  • %s %d IDE brain folder(s), %d CLI brain folder(s), and %d annotation(s)\n",
		prefix, stats.HarvestedIdeBrains, stats.HarvestedCliBrains, stats.HarvestedAnnotations)
	fmt.Printf("  • %s %d missing project definition(s) in config/projects (%s %d duplicate project(s))\n",
		createVerb, stats.ProjectsCreated, strings.ToLower(convVerb), stats.ProjectsConverged)
	for _, name := range stats.CreatedProjectNames {
		fmt.Printf("      + %s\n", name)
	}
	fmt.Printf("  • %s %d cross-machine workspace path(s) and %s %d orphaned conversation(s) to projects\n",
		locVerb, stats.ConversationsLocalized, strings.ToLower(joinVerb), stats.ConversationsJoined)
	fmt.Printf("  • Reconciled %d total conversation summary index(es) (agyhub_summaries_proto.pb + conversation_summaries.db)\n",
		stats.SummariesReconciled)
}

