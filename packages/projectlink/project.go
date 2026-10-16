package projectlink

import "strings"

const ProjectUIDSize = 16
const ProjectPrefix = "coco_"

func NewProjectID(raw [ProjectUIDSize]byte) string {
	return ProjectPrefix + EncodeBase58(raw[:])
}

func ProjectUID(projectID string) ([ProjectUIDSize]byte, bool) {
	var result [ProjectUIDSize]byte
	if !strings.HasPrefix(projectID, ProjectPrefix) || len(projectID) > len(ProjectPrefix)+22 {
		return result, false
	}
	decoded, err := DecodeBase58(strings.TrimPrefix(projectID, ProjectPrefix))
	if err != nil || len(decoded) != ProjectUIDSize {
		return result, false
	}
	copy(result[:], decoded)
	if NewProjectID(result) != projectID {
		return [ProjectUIDSize]byte{}, false
	}
	return result, true
}
