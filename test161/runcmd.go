package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/fatih/color"
	"github.com/ops-class/test161"
	"os"
	"sort"
	"strings"
)

// 'test161 run' flags
var runCommandVars struct {
	dryRun     bool
	explain    bool
	sequential bool
	nodeps     bool
	verbose    string
	isTag      bool
	tests      []string
}

func doRun() int {
	if err := getRunArgs(); err != nil {
		printRunError(err)
		return 1
	}

	exitcode, errs := runTests()
	if len(errs) > 0 {
		printRunErrors(errs)
	}

	return exitcode
}

// Verbose settings
const (
	VERBOSE_LOUD    = "loud"
	VERBOSE_QUIET   = "quiet"
	VERBOSE_WHISPER = "whisper"
)

// Colors
var (
	COLOR_SUCCESS *color.Color = color.New(color.FgGreen)
	COLOR_FAIL    *color.Color = color.New(color.FgRed)
	COLOR_SKIPPED *color.Color = color.New(color.FgBlue)
	COLOR_ABORT   *color.Color = color.New(color.FgBlue)
)

func getRunArgs() error {

	runFlags := flag.NewFlagSet("test161 run", flag.ExitOnError)
	runFlags.Usage = usage

	runFlags.BoolVar(&runCommandVars.dryRun, "dry-run", false, "")
	runFlags.BoolVar(&runCommandVars.dryRun, "d", false, "")
	runFlags.BoolVar(&runCommandVars.explain, "explain", false, "")
	runFlags.BoolVar(&runCommandVars.explain, "x", false, "")
	runFlags.BoolVar(&runCommandVars.sequential, "sequential", false, "")
	runFlags.BoolVar(&runCommandVars.sequential, "s", false, "")
	runFlags.BoolVar(&runCommandVars.nodeps, "no-dependencies", false, "")
	runFlags.BoolVar(&runCommandVars.nodeps, "n", false, "")
	runFlags.StringVar(&runCommandVars.verbose, "verbose", "loud", "")
	runFlags.StringVar(&runCommandVars.verbose, "v", "loud", "")
	runFlags.BoolVar(&runCommandVars.isTag, "tag", false, "")

	runFlags.Parse(os.Args[2:]) // this may exit

	runCommandVars.tests = runFlags.Args()

	if len(runCommandVars.tests) == 0 {
		return errors.New("At least one test or target must be specified")
	}

	switch runCommandVars.verbose {
	case VERBOSE_LOUD:
	case VERBOSE_QUIET:
	case VERBOSE_WHISPER:
	default:
		return errors.New("verbose flag must be one of 'loud', 'quiet', or 'whisper'")
	}

	return nil
}

func runTestGroup(tg *test161.TestGroup, useDeps bool) int {
	var r test161.TestRunner
	if useDeps {
		r = test161.NewDependencyRunner(tg)
	} else {
		r = test161.NewSimpleRunner(tg)
	}

	if runCommandVars.sequential {
		test161.SetManagerCapacity(1)
	} else {
		test161.SetManagerCapacity(0)
	}

	// Set up a PersistenceManager that just outputs to the console
	if runCommandVars.verbose == VERBOSE_LOUD {
		// Compute the max witdth for pretty-printing lines
		max := 0
		for _, t := range tg.Tests {
			if max < len(t.DependencyID) {
				max = len(t.DependencyID)
			}
		}
		env.Persistence = &ConsolePersistence{max}
	}

	// Run it
	test161.StartManager()
	done := r.Run()

	// For reurn val
	allCorrect := true

	for res := range done {
		if res.Test.Result != test161.TEST_RESULT_CORRECT {
			allCorrect = false
		}
		if res.Err != nil {
			fmt.Fprintf(os.Stderr, "Error running %v: %v\n", res.Test.DependencyID, res.Err)
		}
	}

	test161.StopManager()

	printRunSummary(tg, runCommandVars.verbose, useDeps)

	if allCorrect {
		return 0
	} else {
		return 1
	}
}

func printRunSummary(tg *test161.TestGroup, verbosity string, tryDependOrder bool) {
	pd := &PrintData{
		Headings: []*Heading{
			&Heading{
				Text:     "Test",
				MinWidth: 30,
			},
			&Heading{
				Text:     "Result",
				MinWidth: 10,
			},
			&Heading{
				Text:           "Memory Leaks",
				RightJustified: true,
			},
			&Heading{
				Text:           "Score",
				MinWidth:       10,
				RightJustified: true,
			},
		},
		Config: defaultPrintConf,
		Rows:   make(Rows, 0),
	}

	tests := getPrintOrder(tg, tryDependOrder)

	totalPoints, totalAvail := uint(0), uint(0)

	desc := []string{"Total Correct", "Total Incorrect",
		"Total Skipped", "Total Aborted",
	}

	totals := []int{0, 0, 0, 0}

	for _, test := range tests {
		totalPoints += test.PointsEarned
		totalAvail += test.PointsAvailable

		var paint *color.Color = nil

		switch test.Result {
		case test161.TEST_RESULT_CORRECT:
			paint = COLOR_SUCCESS
		case test161.TEST_RESULT_INCORRECT:
			paint = COLOR_FAIL
		case test161.TEST_RESULT_SKIP:
			paint = COLOR_SKIPPED
		case test161.TEST_RESULT_ABORT:
			paint = COLOR_ABORT
		}

		status := string(test.Result)

		if test.Result == test161.TEST_RESULT_SKIP {
			// Try to find a failed dependency
			for _, dep := range test.ExpandedDeps {
				if dep.Result == test161.TEST_RESULT_INCORRECT ||
					dep.Result == test161.TEST_RESULT_SKIP {

					status += " (" + (dep.DependencyID) + ")"
					break
				}
			}
		}

		leak := "---"
		if test.MemLeakChecked {
			if test.MemLeakBytes == 0 {
				leak = "None"
			} else {
				leak = fmt.Sprintf("%v bytes", test.MemLeakBytes)
			}
		}

		row := []*Cell{
			&Cell{Text: test.DependencyID},
			&Cell{Text: status, CellColor: paint},
			&Cell{Text: leak},
			&Cell{Text: fmt.Sprintf("%v/%v", test.PointsEarned, test.PointsAvailable)},
		}

		pd.Rows = append(pd.Rows, row)

		switch test.Result {
		case test161.TEST_RESULT_CORRECT:
			totals[0] += 1
		case test161.TEST_RESULT_INCORRECT:
			totals[1] += 1
		case test161.TEST_RESULT_SKIP:
			totals[2] += 1
		case test161.TEST_RESULT_ABORT:
			totals[3] += 1
		}
	}

	test161.StopManager()

	if verbosity != VERBOSE_WHISPER {
		// Chop off the score if it's not a graded target
		if totalAvail == 0 {
			pd.Headings = pd.Headings[0 : len(pd.Headings)-1]
			for i, row := range pd.Rows {
				pd.Rows[i] = row[0 : len(row)-1]
			}
		}
		fmt.Println()
		pd.Print()
	}

	// Print totals
	fmt.Println()

	for i := 0; i < len(desc); i++ {
		if i == 0 || totals[i] > 0 {
			fmt.Printf("%-15v: %v/%v\n", desc[i], totals[i], len(tg.Tests))
		}
	}

	if totalAvail > 0 {
		fmt.Printf("\n%-15v: %v/%v\n", "Total Score", totalPoints, totalAvail)
	}

	fmt.Println()
}

func runTests() (int, []error) {

	var target *test161.Target
	var ok bool
	exitcode := 0

	// Try running as a Target first
	if len(runCommandVars.tests) == 1 && !runCommandVars.isTag {
		if target, ok = env.Targets[runCommandVars.tests[0]]; ok {
			tg, errs := target.Instance(env)
			if len(errs) > 0 {
				return 1, errs
			} else {
				if runCommandVars.explain {
					exitcode, errs = explain(tg)
				} else if runCommandVars.dryRun {
					printDryRun(tg)
				} else {
					runTestGroup(tg, true)
				}
				return exitcode, errs
			}
		}
	}

	// Not a target, run as a regular (ungraded) TestGroup
	config := &test161.GroupConfig{
		Name:    "test",
		UseDeps: !runCommandVars.nodeps,
		Tests:   runCommandVars.tests,
		Env:     env,
	}

	if tg, errs := test161.GroupFromConfig(config); len(errs) > 0 {
		return 1, errs
	} else {
		if runCommandVars.explain {
			exitcode, errs = explain(tg)
		} else if runCommandVars.dryRun {
			printDryRun(tg)
		} else {
			exitcode = runTestGroup(tg, config.UseDeps)
		}
		return exitcode, errs
	}
}

type testsByID []*test161.Test

func (t testsByID) Len() int           { return len(t) }
func (t testsByID) Swap(i, j int)      { t[i], t[j] = t[j], t[i] }
func (t testsByID) Less(i, j int) bool { return t[i].DependencyID < t[j].DependencyID }

func printDryRun(tg *test161.TestGroup) {

	pd := &PrintData{
		Headings: []*Heading{
			&Heading{
				Text:     "Test ID",
				MinWidth: 30,
			},
			&Heading{
				Text: "Test Name",
			},
			&Heading{
				Text:           "Points",
				RightJustified: true,
			},
		},
		Rows:   make(Rows, 0),
		Config: defaultPrintConf,
	}

	sorted := getPrintOrder(tg, true)

	for _, test := range sorted {
		points := ""
		if test.IsDependency {
			points = "(dependency)"
		} else {
			points = fmt.Sprintf("%v", test.PointsAvailable)
		}
		pd.Rows = append(pd.Rows, []*Cell{
			&Cell{Text: test.DependencyID},
			&Cell{Text: test.Name},
			&Cell{Text: points},
		})
	}

	fmt.Println()
	pd.Print()
	fmt.Println()
}

func explain(tg *test161.TestGroup) (int, []error) {

	tests := getPrintOrder(tg, true)

	fmt.Println()

	for _, test := range tests {
		if test.IsDependency {
			fmt.Printf("%-30v (dependency)\n", test.DependencyID)
		}
	}

	for _, test := range tests {
		if test.IsDependency {
			continue
		}

		fmt.Println()

		// Merge in test161 defaults for any missing configuration values
		test.SetEnv(env)
		if err := test.MergeAllDefaults(); err != nil {
			return 1, []error{err}
		}

		// Test ID
		fmt.Println(test.DependencyID)
		fmt.Println(strings.Repeat("-", 60))
		fmt.Println("Name        :", test.Name)
		fmt.Println("Description :", strings.TrimSpace(test.Description))

		// Monitor
		if test.Monitor.Enabled == "true" {
			fmt.Println("\ntest161 Monitor Conf:")
			fmt.Println("  Progress Timeout :", test.Monitor.ProgressTimeout)
			fmt.Println("  Command Timeout  :", test.Monitor.CommandTimeout)
			fmt.Println("  Window           :", test.Monitor.Window)
			if test.Monitor.Kernel.EnableMin == "true" {
				fmt.Println("  Kernel Min       :", test.Monitor.Kernel.Min)
			} else {
				fmt.Println("  Kernel Min       : disabled")
			}
			fmt.Println("  Kernel Max       :", test.Monitor.Kernel.Max)

			if test.Monitor.User.EnableMin == "true" {
				fmt.Println("  User Min         :", test.Monitor.User.Min)
			} else {
				fmt.Println("  User Min         : disabled")
			}
			fmt.Println("  User Max         :", test.Monitor.User.Max)
		}

		// Sys161
		fmt.Println("\nsys161 Conf:")
		conf, _ := test.PrintConf()
		fmt.Println(strings.TrimSpace(conf))
		fmt.Println()

		// Points/scoring
		if test.PointsAvailable > 0 {
			fmt.Println("Points      : ", test.PointsAvailable)
			fmt.Println("Scoring     : ", test.ScoringMethod)
		}

		// Dependencies
		if len(test.ExpandedDeps) > 0 {
			sorted := make([]*test161.Test, 0)
			for _, dep := range test.ExpandedDeps {
				sorted = append(sorted, dep)
			}
			sort.Sort(testsByID(sorted))

			fmt.Println("Dependencies:")
			for _, dep := range sorted {
				fmt.Println("    ", dep.DependencyID)
			}
		}

		// Commands
		fmt.Println("Commands:")

		for _, cmd := range test.Commands {
			// Instantiate the command so we get the expected output
			fmt.Println("    Cmd Line    :", cmd.Input.Line)
			fmt.Println("      Panics    :", cmd.Panic)
			fmt.Println("      Times Out :", cmd.TimesOut)
			fmt.Println("      Timeout   :", cmd.Timeout)
			fmt.Println("      Points    :", cmd.PointsAvailable)
			if len(cmd.ExpectedOutput) > 0 {
				fmt.Println("      Output    :")
				for _, output := range cmd.ExpectedOutput {
					fmt.Println("            Text     :", output.Text)
					fmt.Println("            Trusted  :", output.Trusted)
					fmt.Println("            KeyID    :", output.KeyName)
				}
			}
		}
	}

	fmt.Println()
	return 0, nil
}

func getPrintOrder(tg *test161.TestGroup, tryDependOrder bool) []*test161.Test {
	tests := make([]*test161.Test, 0, len(tg.Tests))

	if tryDependOrder {
		if graph, err := tg.DependencyGraph(); err == nil {
			if topSort, err := graph.TopSort(); err == nil {

				for i := len(topSort) - 1; i >= 0; i -= 1 {
					if test, ok := tg.Tests[topSort[i]]; !ok {
						break
					} else {
						tests = append(tests, test)
					}
				}
			}

			if len(tests) == len(tg.Tests) {
				return tests
			}
		}
	}

	// Default to alphabetical
	for _, t := range tg.Tests {
		tests = append(tests, t)
	}
	sort.Sort(testsByID(tests))

	return tests
}
