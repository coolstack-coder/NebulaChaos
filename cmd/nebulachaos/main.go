// Command nebulachaos is a Jepsen-style correctness tester for NebulaOS.
//
//	nebulachaos run       one test: workload + fault schedule + linearizability check
//	nebulachaos campaign  many seeded runs, aggregated into a results table
//	nebulachaos zoo       prove the checker catches deliberately planted bugs
//	nebulachaos check     re-check a recorded history.jsonl
//	nebulachaos recheck   re-check every run of a campaign with the current checker
//	nebulachaos report    re-render a run's HTML report
//	nebulachaos selftest  cross-check the WGL checker against Porcupine
package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Fprintln(os.Stderr, `nebulachaos — make NebulaOS lie, then prove it.

usage:
  nebulachaos run      [--target REF] (--schedule FILE | --profile P --seed N) [flags]
  nebulachaos campaign [--target REF] --profiles a,b --seeds 1-20 [flags]
  nebulachaos zoo      [--target REF] [flags]
  nebulachaos check    history.jsonl
  nebulachaos recheck  runs/campaign-…
  nebulachaos report   runs/<run>
  nebulachaos selftest [--n 10000]

run "nebulachaos <command> -h" for flags.`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "campaign":
		err = cmdCampaign(os.Args[2:])
	case "zoo":
		err = cmdZoo(os.Args[2:])
	case "check":
		err = cmdCheck(os.Args[2:])
	case "recheck":
		err = cmdRecheck(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "selftest":
		err = cmdSelftest(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
