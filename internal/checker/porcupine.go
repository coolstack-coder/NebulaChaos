package checker

import (
	"fmt"
	"time"

	"github.com/anishathalye/porcupine"
	"github.com/coolstack-coder/nebulachaos/internal/history"
)

// The same register model expressed for Porcupine. Inputs carry the whole
// op so Step can reuse the model above; outputs are unused.
var registerModel = porcupine.Model{
	Partition: func(h []porcupine.Operation) [][]porcupine.Operation {
		by := map[string][]porcupine.Operation{}
		var keys []string
		for _, o := range h {
			k := o.Input.(history.Op).Key
			if _, ok := by[k]; !ok {
				keys = append(keys, k)
			}
			by[k] = append(by[k], o)
		}
		out := make([][]porcupine.Operation, 0, len(keys))
		for _, k := range keys {
			out = append(out, by[k])
		}
		return out
	},
	Init: func() any { return absent },
	Step: func(state, input, _ any) (bool, any) {
		return step(state.(string), input.(history.Op))
	},
	Equal: func(a, b any) bool { return a.(string) == b.(string) },
	DescribeOperation: func(input, _ any) string {
		return input.(history.Op).String()
	},
	DescribeState: func(state any) string {
		if state.(string) == absent {
			return "∅"
		}
		return state.(string)
	},
}

// toPorcupine converts a prepared history. Indeterminate operations are
// given a return time after everything else, the standard way to say
// "this may take effect at any point after it was invoked, or never".
func toPorcupine(ops []history.Op) []porcupine.Operation {
	var end int64
	for _, o := range ops {
		end = max(end, o.Complete, o.Invoke)
	}
	out := make([]porcupine.Operation, 0, len(ops))
	for _, o := range ops {
		ret := o.Complete
		if o.Status == history.Info {
			ret = end + 1
		}
		out = append(out, porcupine.Operation{ClientId: o.Process, Input: o, Call: o.Invoke, Return: ret})
	}
	return out
}

// CheckPorcupine runs Porcupine on a (multi-key) history.
func CheckPorcupine(ops []history.Op, timeout time.Duration) (Result, porcupine.LinearizationInfo) {
	res, info := porcupine.CheckOperationsVerbose(registerModel, toPorcupine(prepare(ops)), timeout)
	switch res {
	case porcupine.Ok:
		return Linearizable, info
	case porcupine.Illegal:
		return Violation, info
	}
	return Unknown, info
}

// Visualize writes Porcupine's interactive HTML view of a history.
func Visualize(path string, info porcupine.LinearizationInfo) error {
	if err := porcupine.VisualizePath(registerModel, info, path); err != nil {
		return fmt.Errorf("porcupine visualization: %w", err)
	}
	return nil
}
