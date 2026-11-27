package zelio

import (
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

func fbdTestRecord(op byte, words ...uint16) []byte {
	data := []byte{op}
	for _, word := range words {
		data = binary.LittleEndian.AppendUint16(data, word)
	}
	return data
}

func fbdTestLinks(links ...[2]uint16) []byte {
	data := make([]byte, 16*256)
	var offset int
	for _, link := range links {
		offset += copy(data[offset:], fbdTestRecord(0x0b, link[0], link[1]))
		offset++
	}
	offset += copy(data[offset:], fbdTestRecord(0x0b, 80, 90))
	offset++
	offset += copy(data[offset:], fbdTestRecord(0x0b, 81, 91))
	data[offset] = 1
	return data
}

func fbdTestProgram(records ...[]byte) []byte {
	data := make([]byte, 16*256)
	var offset int
	for _, record := range records {
		offset += copy(data[offset:], record)
	}
	return data
}

func fbdTestRoute(t *testing.T, result FBDDiscovery, signal uint8) FBDRoute {
	t.Helper()
	for _, route := range result.Routes {
		if route.SignalID == signal {
			return route
		}
	}
	t.Fatalf("missing signal 0x%02x in routes %+v", signal, result.Routes)
	return FBDRoute{}
}

func TestDecodeMonitoringFBDToggleFanout(t *testing.T) {
	links := fbdTestLinks([2]uint16{1, 0}, [2]uint16{7, 5}, [2]uint16{70, 11})
	program := fbdTestProgram(
		fbdTestRecord(0x23, 1, 0, 0x2700),
		fbdTestRecord(0x23, 7, 5, 0x2701),
		fbdTestRecord(0x19, 70, 5, 90, 90, 90, 11),
		fbdTestRecord(0x01, 72, 11, 0, 20, 1),
		fbdTestRecord(0x01, 73, 11, 0, 21, 2),
		fbdTestRecord(0x26, 35, 21, 0x2704),
		fbdTestRecord(0x26, 34, 20, 0x2703),
		fbdTestRecord(0x26, 0, 90, 0x2702),
	)
	result, err := DecodeMonitoringFBD(links, program)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"QA", "QB"}
	if !reflect.DeepEqual(result.Outputs, want) {
		t.Fatalf("wired outputs = %v, want %v", result.Outputs, want)
	}
	for _, signal := range []uint8{5, 11} {
		route := fbdTestRoute(t, result, signal)
		if route.Kind != FBDToggleCandidate || !reflect.DeepEqual(route.Outputs, want) {
			t.Fatalf("signal %d = %+v, want a toggle candidate for %v", signal, route, want)
		}
	}
	route := fbdTestRoute(t, result, 5)
	if route.Block != 7 || route.Input != "I6" {
		t.Fatalf("physical input route = %+v, want B007/I6", route)
	}
	reset := fbdTestRoute(t, result, 0)
	if reset.Kind != FBDReset || reset.Input != "I1" || !reflect.DeepEqual(reset.Outputs, want) || !reflect.DeepEqual(reset.ResetOutputs, want) {
		t.Fatalf("zero signal = %+v, want an I1 reset route for %v", reset, want)
	}
}

func TestDecodeMonitoringFBDResetBranchTakesPrecedence(t *testing.T) {
	program := fbdTestProgram(
		fbdTestRecord(0x23, 1, 5, 0x2700),
		fbdTestRecord(0x19, 60, 5, 90, 90, 90, 19),
		fbdTestRecord(0x01, 62, 5, 90, 20, 1),
		fbdTestRecord(0x01, 63, 90, 19, 21, 2),
		fbdTestRecord(0x26, 0, 20, 0x2701),
		fbdTestRecord(0x26, 26, 21, 0x2702),
	)
	result, err := DecodeMonitoringFBD(fbdTestLinks([2]uint16{1, 5}), program)
	if err != nil {
		t.Fatal(err)
	}
	route := fbdTestRoute(t, result, 5)
	if route.Kind != FBDReset || !reflect.DeepEqual(route.Outputs, []string{"Q1", "Q2"}) {
		t.Fatalf("mixed branches = %+v, want a reset route for Q1/Q2", route)
	}
	if !reflect.DeepEqual(route.ResetOutputs, []string{"Q2"}) {
		t.Fatalf("mixed branches reset outputs = %v, want Q2 only", route.ResetOutputs)
	}
}

func TestDecodeMonitoringFBDResetInterlockRequiresActiveHighPath(t *testing.T) {
	program := fbdTestProgram(
		fbdTestRecord(0x23, 1, 5, 0x2700),
		fbdTestRecord(0x17, 60, 5, 18),
		fbdTestRecord(0x02, 61, 5, 19, 90, 90, 20),
		fbdTestRecord(0x01, 62, 90, 18, 21, 1),
		fbdTestRecord(0x01, 63, 90, 20, 22, 2),
		fbdTestRecord(0x26, 0, 21, 0x2701),
		fbdTestRecord(0x26, 26, 22, 0x2702),
	)
	result, err := DecodeMonitoringFBD(fbdTestLinks([2]uint16{1, 5}), program)
	if err != nil {
		t.Fatal(err)
	}
	route := fbdTestRoute(t, result, 5)
	if route.Kind != FBDReset || !reflect.DeepEqual(route.Outputs, []string{"Q1", "Q2"}) {
		t.Fatalf("inverted and conditional reset = %+v, want a reset route affecting Q1/Q2", route)
	}
	if len(route.ResetOutputs) != 0 {
		t.Fatalf("inverted and conditional reset interlocks = %v, want no proven active-high interlock", route.ResetOutputs)
	}
}

func TestDecodeMonitoringFBDAmbiguousRoute(t *testing.T) {
	for _, test := range []struct {
		name    string
		records [][]byte
	}{
		{
			name: "direct output",
			records: [][]byte{
				fbdTestRecord(0x26, 0, 5, 0x2701),
			},
		},
		{
			name: "stateful branch with no physical output",
			records: [][]byte{
				fbdTestRecord(0x01, 60, 5, 90, 21, 1),
				fbdTestRecord(0x01, 61, 5, 90, 22, 2),
				fbdTestRecord(0x26, 0, 21, 0x2701),
			},
		},
		{
			name: "unknown OR operand",
			records: [][]byte{
				fbdTestRecord(0x19, 60, 5, 89, 90, 90, 20),
				fbdTestRecord(0x01, 61, 20, 90, 21, 1),
				fbdTestRecord(0x26, 0, 21, 0x2701),
			},
		},
		{
			name: "unknown reset operand",
			records: [][]byte{
				fbdTestRecord(0x01, 60, 5, 89, 20, 1),
				fbdTestRecord(0x26, 0, 20, 0x2701),
			},
		},
		{
			name: "always-high reset operand",
			records: [][]byte{
				fbdTestRecord(0x01, 60, 5, 91, 20, 1),
				fbdTestRecord(0x26, 0, 20, 0x2701),
			},
		},
		{
			name: "AND before bistable",
			records: [][]byte{
				fbdTestRecord(0x02, 60, 5, 30, 90, 90, 20),
				fbdTestRecord(0x01, 61, 20, 90, 21, 1),
				fbdTestRecord(0x26, 0, 21, 0x2701),
			},
		},
		{
			name: "opaque side effect",
			records: [][]byte{
				fbdTestRecord(0x01, 60, 5, 90, 20, 1),
				fbdTestRecord(0x04, 62, 5, 0x03f2, 0x2702, 0, 0x2703),
				fbdTestRecord(0x26, 0, 20, 0x2701),
			},
		},
		{
			name: "cycle",
			records: [][]byte{
				fbdTestRecord(0x19, 60, 5, 20, 90, 90, 20),
				fbdTestRecord(0x01, 61, 20, 90, 21, 1),
				fbdTestRecord(0x26, 0, 21, 0x2701),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			records := append([][]byte{fbdTestRecord(0x23, 1, 5, 0x2700)}, test.records...)
			result, err := DecodeMonitoringFBD(fbdTestLinks([2]uint16{1, 5}), fbdTestProgram(records...))
			if err != nil {
				t.Fatal(err)
			}
			route := fbdTestRoute(t, result, 5)
			if route.Kind != FBDUnresolved || route.Reason == "" {
				t.Fatalf("ambiguous route = %+v, want unresolved with reason", route)
			}
		})
	}
}

func TestDecodeMonitoringFBDRejectsUnknownOperandsOfSourceOR(t *testing.T) {
	links := fbdTestLinks([2]uint16{60, 20})
	program := fbdTestProgram(
		fbdTestRecord(0x23, 1, 5, 0x2700),
		fbdTestRecord(0x19, 60, 5, 89, 90, 90, 20),
		fbdTestRecord(0x01, 61, 20, 90, 21, 1),
		fbdTestRecord(0x26, 0, 21, 0x2701),
	)
	result, err := DecodeMonitoringFBD(links, program)
	if err != nil {
		t.Fatal(err)
	}
	if route := fbdTestRoute(t, result, 20); route.Kind != FBDUnresolved {
		t.Fatalf("source OR has an unknown input: %+v, want unresolved", route)
	}
}

func TestDecodeMonitoringFBDRejectsUnknownOrConflictingProgram(t *testing.T) {
	for _, test := range []struct {
		name    string
		records [][]byte
		message string
	}{
		{
			name:    "unknown opcode",
			records: [][]byte{{0xff}},
			message: "opcode",
		},
		{
			name:    "nonzero after terminator",
			records: [][]byte{{0, 0x01}},
			message: "padding",
		},
		{
			name:    "conflicting producer",
			records: [][]byte{fbdTestRecord(0x23, 3, 5, 0x2702)},
			message: "signal",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			records := [][]byte{
				fbdTestRecord(0x23, 1, 5, 0x2700),
				fbdTestRecord(0x26, 0, 5, 0x2701),
			}
			records = append(records, test.records...)
			result, err := DecodeMonitoringFBD(fbdTestLinks([2]uint16{1, 5}), fbdTestProgram(records...))
			if err == nil || !strings.Contains(err.Error(), test.message) || len(result.Routes) != 0 {
				t.Fatalf("unsupported program = %+v, %v, want %q error without routes", result, err, test.message)
			}
		})
	}
}

func TestDecodeMonitoringFBDDoesNotTrustLinkRecordWithoutMatchingSource(t *testing.T) {
	for _, links := range [][]byte{
		fbdTestLinks([2]uint16{3, 5}),
		fbdTestLinks([2]uint16{1, 5}, [2]uint16{3, 5}),
	} {
		program := fbdTestProgram(
			fbdTestRecord(0x23, 1, 5, 0x2700),
			fbdTestRecord(0x01, 60, 5, 90, 20, 1),
			fbdTestRecord(0x26, 0, 20, 0x2701),
		)
		result, err := DecodeMonitoringFBD(links, program)
		if err != nil {
			t.Fatal(err)
		}
		route := fbdTestRoute(t, result, 5)
		if route.Kind != FBDUnresolved || len(route.Outputs) != 0 {
			t.Fatalf("unconfirmed source = %+v, want unresolved without outputs", route)
		}
	}
}
