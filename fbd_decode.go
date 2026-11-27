package zelio

import (
	"encoding/binary"
	"fmt"
	"sort"
)

const fbdBankImageSize = fbdBankCount * fbdBankSize

// FBDRouteKind describes the static paths from a monitoring link.
type FBDRouteKind string

const (
	// FBDToggleCandidate reaches physical outputs through bistable toggle inputs.
	FBDToggleCandidate FBDRouteKind = "toggle_candidate"
	// FBDReset reaches at least one bistable reset input.
	FBDReset FBDRouteKind = "reset"
	// FBDUnresolved lacks a complete, unambiguous toggle path.
	FBDUnresolved FBDRouteKind = "unresolved"
)

// FBDRoute describes one link-table entry and the outputs reachable from it.
// A candidate describes the compiled logic and grants no monitoring write access.
type FBDRoute struct {
	SignalID uint8        `json:"signal_id"`
	Block    uint16       `json:"block"`
	Input    string       `json:"input,omitempty"`
	Outputs  []string     `json:"outputs,omitempty"`
	Kind     FBDRouteKind `json:"kind"`
	Reason   string       `json:"reason,omitempty"`
	// ResetOutputs contains physical outputs with a proven active-high
	// reset path from Input to a bistable directly driving the output.
	ResetOutputs []string `json:"reset_outputs,omitempty"`
}

// FBDDiscovery reports physical outputs with a decoded signal source and the
// links advertised by the program. Outputs are sorted by label.
type FBDDiscovery struct {
	Outputs []string   `json:"outputs"`
	Routes  []FBDRoute `json:"routes"`
}

const (
	fbdBistable byte = 0x01
	fbdAnd      byte = 0x02
	fbdTimer    byte = 0x06
	fbdNot      byte = 0x17
	fbdOr       byte = 0x19
	fbdEdge     byte = 0x1a
	fbdRS       byte = 0x1c
	fbdInput    byte = 0x23
	fbdOutput   byte = 0x26
)

type fbdNode struct {
	opcode    byte
	block     uint16
	inputs    []uint16
	signal    uint16
	hasSignal bool
	label     string
}

type fbdConsumer struct {
	node  fbdNode
	input int
}

type fbdProgram struct {
	producers     map[uint16]fbdNode
	consumers     map[uint16][]fbdConsumer
	outputs       map[string]uint16
	zeroSignal    uint16
	hasZeroSignal bool
}

type fbdLinkSlot struct {
	count int
	flag  byte
}

// DecodeMonitoringFBD decodes the observed Zelio FBD layout from complete
// snapshots of regions 0x00–0x0F and 0x18–0x27, respectively. Each region
// contributes 256 bytes in increasing order. The supported layout was
// checked against SR3B261FU + SR3XT141FU FBD programs. Runtime I/O, timing,
// physical loads and momentary frame behavior require separate evidence.
func DecodeMonitoringFBD(linkTable, program []byte) (FBDDiscovery, error) {
	if len(linkTable) != fbdBankImageSize || len(program) != fbdBankImageSize {
		return FBDDiscovery{}, fmt.Errorf("%w: FBD bank images must both be %d bytes", ErrProtocol, fbdBankImageSize)
	}
	routes, slots, err := parseFBDLinkTable(linkTable)
	if err != nil {
		return FBDDiscovery{}, err
	}
	graph, err := parseFBDProgram(program)
	if err != nil {
		return FBDDiscovery{}, err
	}
	graph.zeroSignal, graph.hasZeroSignal = graph.findZeroSignal(slots)
	result := FBDDiscovery{Outputs: []string{}, Routes: routes}
	for label, signal := range graph.outputs {
		if _, ok := graph.producers[signal]; ok {
			result.Outputs = append(result.Outputs, label)
		}
	}
	sort.Strings(result.Outputs)
	for i := range result.Routes {
		route := &result.Routes[i]
		source, ok := graph.producers[uint16(route.SignalID)]
		switch {
		case slots[route.SignalID].count > 1:
			route.Reason = "signal ID has multiple link records"
		case !ok || source.block != route.Block:
			route.Reason = "link has no matching signal producer in the compiled program"
		default:
			graph.describeRoute(route, source)
		}
	}
	sort.Slice(result.Routes, func(i, j int) bool {
		left, right := result.Routes[i], result.Routes[j]
		if left.SignalID == right.SignalID {
			return left.Block < right.Block
		}
		return left.SignalID < right.SignalID
	})
	return result, nil
}

func parseFBDLinkTable(raw []byte) ([]FBDRoute, map[uint8]fbdLinkSlot, error) {
	routes := []FBDRoute{}
	slots := make(map[uint8]fbdLinkSlot)
	for offset := 0; offset < len(raw); {
		opcode := raw[offset]
		var size int
		switch opcode {
		case 0:
			if offset+1 == len(raw) || raw[offset+1] != 0 {
				return nil, nil, fmt.Errorf("%w: invalid FBD link table terminator at 0x%X", ErrProtocol, offset)
			}
			// Additional program metadata may follow the two-byte terminator.
			return routes, slots, nil
		case 0x0b, 0x0e:
			size = 6
		case 0x0c, 0x13:
			size = 7
		case 0x0f:
			size = 8
		case 0x10:
			size = 9
		default:
			return nil, nil, fmt.Errorf("%w: unsupported FBD link table opcode 0x%02X at 0x%X", ErrProtocol, opcode, offset)
		}
		if len(raw)-offset < size {
			return nil, nil, fmt.Errorf("%w: truncated FBD link table record at 0x%X", ErrProtocol, offset)
		}
		record := raw[offset : offset+size]
		if opcode == 0x0b && record[4] == 0 {
			signalID, flag := record[3], record[5]
			slots[signalID] = fbdLinkSlot{count: slots[signalID].count + 1, flag: flag}
			if flag == 0 {
				routes = append(routes, FBDRoute{
					SignalID: signalID,
					Block:    binary.LittleEndian.Uint16(record[1:3]),
					Kind:     FBDUnresolved,
				})
			}
		}
		offset += size
	}
	return nil, nil, fmt.Errorf("%w: missing FBD link table terminator", ErrProtocol)
}

func (graph fbdProgram) findZeroSignal(slots map[uint8]fbdLinkSlot) (uint16, bool) {
	// The compiler appends adjacent constant slots after the digital
	// signals. Their link flags are 0 and 1, in either order. The 0 slot
	// appears as the source for unconnected OR pins and physical outputs.
	var last uint8
	for signal := range slots {
		last = max(last, signal)
	}
	if last == 0 {
		return 0, false
	}
	prevSlot, lastSlot := slots[last-1], slots[last]
	if prevSlot.count != 1 || lastSlot.count != 1 || prevSlot.flag > 1 || lastSlot.flag > 1 || prevSlot.flag == lastSlot.flag {
		return 0, false
	}
	for signal := range graph.producers {
		if signal >= uint16(last-1) {
			return 0, false
		}
	}
	if prevSlot.flag == 0 {
		return uint16(last - 1), true
	}
	return uint16(last), true
}

func (graph fbdProgram) knownInputs(node fbdNode) bool {
	for _, signal := range node.inputs {
		if _, ok := graph.producers[signal]; !ok && (!graph.hasZeroSignal || signal != graph.zeroSignal) {
			return false
		}
	}
	return true
}

func parseFBDProgram(raw []byte) (fbdProgram, error) {
	graph := fbdProgram{
		producers: make(map[uint16]fbdNode),
		consumers: make(map[uint16][]fbdConsumer),
		outputs:   make(map[string]uint16),
	}
	blocks := make(map[uint16]bool)
	terminated := false
	for offset := 0; offset < len(raw); {
		opcode := raw[offset]
		if opcode == 0 {
			for _, value := range raw[offset:] {
				if value != 0 {
					return fbdProgram{}, fmt.Errorf("%w: nonzero FBD program padding after 0x%X", ErrProtocol, offset)
				}
			}
			terminated = true
			break
		}
		wordCount := fbdProgramWordCount(opcode)
		if wordCount == 0 {
			return fbdProgram{}, fmt.Errorf("%w: unsupported FBD program opcode 0x%02X at 0x%X", ErrProtocol, opcode, offset)
		}
		size := 1 + 2*wordCount
		if len(raw)-offset < size {
			return fbdProgram{}, fmt.Errorf("%w: truncated FBD program record at 0x%X", ErrProtocol, offset)
		}
		words := make([]uint16, wordCount)
		for i := range words {
			words[i] = binary.LittleEndian.Uint16(raw[offset+1+2*i:])
		}
		node := decodeFBDNode(opcode, words)
		if blocks[node.block] {
			return fbdProgram{}, fmt.Errorf("%w: duplicate FBD program block B%03d", ErrProtocol, node.block)
		}
		blocks[node.block] = true
		if node.hasSignal {
			if _, ok := graph.producers[node.signal]; ok {
				return fbdProgram{}, fmt.Errorf("%w: duplicate FBD signal 0x%04X", ErrProtocol, node.signal)
			}
			graph.producers[node.signal] = node
		}
		if node.opcode == fbdOutput {
			if node.label == "" {
				return fbdProgram{}, fmt.Errorf("%w: unknown FBD physical output block B%03d", ErrProtocol, node.block)
			}
			graph.outputs[node.label] = node.inputs[0]
		}
		for i, signal := range node.inputs {
			graph.consumers[signal] = append(graph.consumers[signal], fbdConsumer{node: node, input: i})
		}
		offset += size
	}
	if !terminated {
		return fbdProgram{}, fmt.Errorf("%w: missing FBD program terminator", ErrProtocol)
	}
	if len(graph.outputs) == 0 {
		return fbdProgram{}, fmt.Errorf("%w: FBD program has no decoded physical outputs", ErrProtocol)
	}
	return graph, nil
}

func fbdProgramWordCount(opcode byte) int {
	switch opcode {
	case fbdNot, fbdInput, fbdOutput, 0x31:
		return 3
	case fbdBistable, fbdEdge, fbdRS:
		return 5
	case fbdAnd, fbdOr, 0x04:
		return 6
	case 0x5f, 0x60:
		return 10
	case fbdTimer:
		return 12
	case 0x56:
		return 18
	default:
		return 0
	}
}

func decodeFBDNode(opcode byte, words []uint16) fbdNode {
	node := fbdNode{opcode: opcode, block: words[0], hasSignal: true}
	switch opcode {
	case fbdInput:
		node.signal = words[1]
		switch {
		case node.block == 1:
			node.label = "I1"
		case node.block >= 3 && node.block <= 25:
			node.label = "I" + string(monitorInputLabels[node.block-2])
		}
	case fbdOutput:
		node.hasSignal = false
		node.inputs = words[1:2]
		switch {
		case node.block == 0:
			node.label = "Q1"
		case node.block >= 26 && node.block <= 40:
			node.label = "Q" + string(monitorOutputLabels[node.block-25])
		}
	case fbdBistable, fbdRS:
		node.inputs, node.signal = words[1:3], words[3]
	case fbdOr, fbdAnd:
		node.inputs, node.signal = words[1:5], words[5]
	case fbdNot, fbdEdge:
		node.inputs, node.signal = words[1:2], words[2]
	case fbdTimer:
		// Only the first timer result was cross-checked against its link
		// records. Any possible timer operand disqualifies a toggle path.
		node.signal = words[3]
		node.inputs = append(append([]uint16(nil), words[1:3]...), words[4:]...)
	default:
		// These record widths are known. Conservatively treat each operand
		// as a possible consumer since the full pin layout is unverified.
		node.hasSignal = false
		node.inputs = words[1:]
	}
	return node
}

type fbdTracePhase uint8

const (
	fbdBeforeToggle fbdTracePhase = iota
	fbdAfterToggle
	fbdAfterInputReset
	fbdAmbiguous
)

type fbdTraceKey struct {
	signal uint16
	phase  fbdTracePhase
}

func (graph fbdProgram) describeRoute(route *FBDRoute, source fbdNode) {
	if source.opcode == fbdInput {
		route.Input = source.label
	}
	active := make(map[uint16]bool)
	visited := make(map[fbdTraceKey]bool)
	outputs := make(map[string]bool)
	resetOutputs := make(map[string]bool)
	reset := false
	var ambiguous bool
	switch source.opcode {
	case fbdInput:
		ambiguous = source.label == ""
	case fbdOr:
		ambiguous = !graph.knownInputs(source)
	default:
		ambiguous = true
	}
	var visit func(uint16, fbdTracePhase)
	visit = func(signal uint16, phase fbdTracePhase) {
		if active[signal] {
			ambiguous = true
			return
		}
		key := fbdTraceKey{signal, phase}
		if visited[key] {
			return
		}
		active[signal], visited[key] = true, true
		uses := graph.consumers[signal]
		if len(uses) == 0 {
			ambiguous = true
		}
		for _, use := range uses {
			node, next := use.node, fbdAmbiguous
			switch node.opcode {
			case fbdOutput:
				outputs[node.label] = true
				if phase == fbdAfterInputReset {
					resetOutputs[node.label] = true
				}
				ambiguous = ambiguous || phase != fbdAfterToggle
				continue
			case fbdOr:
				if phase == fbdBeforeToggle && graph.knownInputs(node) {
					next = fbdBeforeToggle
				}
			case fbdBistable:
				// The first pin toggles the state. The second pin resets it.
				if use.input == 1 {
					reset = true
					if phase == fbdBeforeToggle && route.Input != "" && graph.knownInputs(node) {
						next = fbdAfterInputReset
					}
				} else if phase == fbdBeforeToggle && graph.knownInputs(node) {
					next = fbdAfterToggle
				}
			}
			ambiguous = ambiguous || next == fbdAmbiguous
			if node.hasSignal {
				visit(node.signal, next)
			}
		}
		delete(active, signal)
	}
	visit(source.signal, fbdBeforeToggle)
	route.Outputs = sortedFBDLabels(outputs)
	route.ResetOutputs = sortedFBDLabels(resetOutputs)
	switch {
	case reset:
		route.Kind, route.Reason = FBDReset, "reaches a bistable reset input"
	case len(route.Outputs) == 0:
		route.Reason = "no decoded path to a physical output"
	case ambiguous:
		route.Reason = "path includes unsupported, mixed or incomplete logic"
	default:
		route.Kind = FBDToggleCandidate
	}
}

func sortedFBDLabels(labels map[string]bool) []string {
	var result []string
	for label := range labels {
		result = append(result, label)
	}
	sort.Strings(result)
	return result
}
