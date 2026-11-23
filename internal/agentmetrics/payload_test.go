package agentmetrics

import (
	"strings"
	"testing"
)

func envelope(data string) []byte {
	return []byte(`{"version":"brine.agent.read/1","requestId":"id","data":` + data + `}`)
}

const base = `"capability":"brine.agent.metrics.aggregate/1","coverage":"partial","hostMemoryComplete":true,"ownedMemoryComplete":false,"hostMemoryTotalBytes":1024,"hostMemoryAvailableBytes":0`

func TestSampleValid(t *testing.T) {
	cases := []string{
		`{` + base + `}`,
		`{` + base + `,"ownedRunningContainers":0}`,
		`{"capability":"brine.agent.metrics.aggregate/1","coverage":"complete","hostMemoryComplete":true,"ownedMemoryComplete":true,"hostMemoryTotalBytes":1024,"hostMemoryAvailableBytes":1024,"ownedRunningContainers":9007199254740991,"ownedMemoryUsageBytes":0}`,
		`{"capability":"brine.agent.metrics.aggregate/1","coverage":"partial","hostMemoryComplete":false,"ownedMemoryComplete":true,"ownedRunningContainers":1,"ownedMemoryUsageBytes":9007199254740991}`,
	}
	for _, s := range cases {
		if _, err := decodeSample(envelope(s), "id"); err != nil {
			t.Errorf("valid %s: %v", s, err)
		}
	}
	s, err := decodeSample(envelope(`{`+base+`,"ownedRunningContainers":0}`), "id")
	if err != nil || !s.HasHost || !s.HasOwnedCount || s.OwnedCount != 0 || s.HostAvailable != 0 {
		t.Fatal(s, err)
	}
}
func TestSampleReject(t *testing.T) {
	good := string(envelope(`{` + base + `}`))
	cases := []string{
		strings.Replace(good, `"version"`, `"Version"`, 1), strings.Replace(good, `"id"`, `"other"`, 1), good + ` {}`,
		strings.Replace(good, `"coverage":"partial"`, `"coverage":"complete"`, 1),
		strings.Replace(good, `"hostMemoryComplete":true`, `"hostMemoryComplete":false`, 1),
		strings.Replace(good, `"hostMemoryAvailableBytes":0`, `"hostMemoryAvailableBytes":null`, 1),
		strings.Replace(good, `"hostMemoryAvailableBytes":0`, `"hostMemoryAvailableBytes":1025`, 1),
		strings.Replace(good, `"hostMemoryTotalBytes":1024`, `"hostMemoryTotalBytes":0`, 1),
		strings.Replace(good, `"hostMemoryTotalBytes":1024`, `"hostMemoryTotalBytes":9007199254740992`, 1),
		strings.Replace(good, `"hostMemoryTotalBytes":1024`, `"hostMemoryTotalBytes":1e3`, 1),
		strings.Replace(good, `"hostMemoryTotalBytes":1024`, `"hostMemoryTotalBytes":-1`, 1),
		strings.Replace(good, `"hostMemoryAvailableBytes":0`, `"hostMemoryAvailableBytes":0,"hostMemoryAvailableBytes":0`, 1),
		strings.Replace(good, `"hostMemoryAvailableBytes":0`, `"HostMemoryAvailableBytes":0`, 1),
		strings.Replace(good, `"hostMemoryAvailableBytes":0`, `"hostMemoryAvailableBytes":0,"extra":1`, 1),
		strings.Replace(good, `"hostMemoryAvailableBytes":0`, `"hostMemoryAvailableBytes":0,"ownedRunningContainers":0,"ownedMemoryUsageBytes":0`, 1),
		strings.Replace(good, `"hostMemoryAvailableBytes":0`, `"hostMemoryAvailableBytes":0,"ownedMemoryUsageBytes":1`, 1),
		strings.Replace(good, `"hostMemoryAvailableBytes":0`, `"hostMemoryAvailableBytes":0,"x":{"a":{"b":{"c":1}}}`, 1),
		string([]byte{0xff}), strings.Repeat(" ", 4097),
	}
	for _, s := range cases {
		if _, err := decodeSample([]byte(s), "id"); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if _, err := decodeSample(envelope(`{"capability":"brine.agent.metrics.aggregate/1","coverage":"partial","hostMemoryComplete":false,"ownedMemoryComplete":false,"ownedRunningContainers":0}`), "id"); err == nil {
		t.Fatal("count alone")
	}
}
