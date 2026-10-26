// Package nemesis turns a fault schedule into actions on a running
// cluster: partitions, pauses, crashes, power cuts and disk corruption.
package nemesis

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Dur is a time.Duration that reads and writes as "1.5s" in YAML.
type Dur time.Duration

func (d Dur) D() time.Duration { return time.Duration(d) }

func (d *Dur) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	*d = Dur(v)
	return err
}

func (d Dur) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Event is one step of a schedule.
//
//	do: partition  target: isolate-leader | leader-alone | halves | ring | bridge
//	do: heal
//	do: delay      target: all | <node>   delay: 150ms
//	do: pause      target: <node>         (resume with do: resume, or set for:)
//	do: kill       target: <node>         (restart with do: start, or set for:)
//	do: powerloss  target: <node> | all   (power cut, unsynced writes lost, restarted)
//	do: flip-log | corrupt-snapshot | snapshot-header | delete-snapshot  target: <node>
//
// A node selector is n1..n5, leader, follower or random; it is resolved when
// the event fires and the concrete node is written to the run's nemesis log.
type Event struct {
	At     Dur    `yaml:"at"`
	Do     string `yaml:"do"`
	Target string `yaml:"target,omitempty"`
	For    Dur    `yaml:"for,omitempty"`
	Delay  Dur    `yaml:"delay,omitempty"`
}

// Schedule is a replayable fault plan.
type Schedule struct {
	Name        string  `yaml:"name"`
	Description string  `yaml:"description,omitempty"`
	Duration    Dur     `yaml:"duration"`
	Seed        uint64  `yaml:"seed,omitempty"`
	Events      []Event `yaml:"events"`
}

func Load(path string) (*Schedule, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Schedule
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Duration == 0 {
		return nil, fmt.Errorf("%s: duration is required", path)
	}
	return &s, nil
}

func (s *Schedule) Save(path string) error {
	b, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
