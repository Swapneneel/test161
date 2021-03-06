package test161

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	yaml "gopkg.in/yaml.v2"
	"io/ioutil"
	"os"
	"strings"
)

// For simple cases, it is annoying to have to specify the points for the test
// and the command.  So, if the test is made up of only one command, there is
// no reason to specify the command.  So, some rules and convention:
//
// 1) Points must always be specified in the Target (verification)
// 2) Points must always be specified in the Test   (verification)
// 3) Points can be ommitted from the commands, provided that:
//		a) test points - sum(assigned points) % (remaining points) == 0
//		   (i.e. no fractional points per test)

const (
	TARGET_ASST = "asst"
	TARGET_PERF = "perf"
)

const (
	TEST_SCORING_ENTIRE  = "entire"
	TEST_SCORING_PARTIAL = "partial"
)

// A test161 Target is the sepcification for group of related tests. Currently,
// we support two types of Targets with special meaning: asst and perf. The main
// difference between Targets and TestGroups is that Targets can have a
// scoring component, either points or performance. The test161 submission system
// operates in terms of Targets.
//
// 02/2017 - Targets can now be MetaTargets which have a list of subtargets. Subtargets
//           are runable, whereas metatargets are not.
type Target struct {
	// Make sure to update isChangeAllowed with any new fields that need to be versioned.
	ID               string        `yaml:"-" bson:"_id"`
	Name             string        `yaml:"name"`
	Active           string        `yaml:"active"`
	Version          uint          `yaml:"version"`
	Type             string        `yaml:"type"`
	Points           uint          `yaml:"points"`
	KConfig          string        `yaml:"kconfig"`
	RequiredCommit   string        `yaml:"required_commit" bson:"required_commit"`
	RequiresUserland bool          `yaml:"userland" bson:"userland"`
	Tests            []*TargetTest `yaml:"tests"`
	FileHash         string        `yaml:"-" bson:"file_hash"`
	FileName         string        `yaml:"-" bson:"file_name"`

	// MetaTarget info
	IsMetaTarget   bool     `yaml:"is_meta_target" bson:"is_meta_target"`
	SubTargetNames []string `yaml:"sub_target_names" bson:"sub_target_names"`
	MetaName       string   `yaml:"meta_name"`

	// Front-end only
	PrintName   string `yaml:"print_name" bson:"print_name"`
	Description string `yaml:"description"`
	Link        string `yaml:"link" bson:"link"`
	Leaderboard string `yaml:"leaderboard" bson:"leaderboard"`

	// Parent and siblings if this is a subtarget of a metatarget.
	metaTarget         *Target
	previousSubTargets []*Target
}

// A TargetTest is the specification for a single Test contained in the Target.
// Currently, the Test can only appear in the Target once.
type TargetTest struct {
	Id            string           `yaml:"id" bson:"test_id"`
	Scoring       string           `yaml:"scoring"`
	Points        uint             `yaml:"points"`
	MemLeakPoints uint             `yaml:"mem_leak_points"`
	Commands      []*TargetCommand `yaml:"commands"`
}

// TargetCommands (optionally) specify information about the commands contained
// in TargetTests. TargetCommands allow you to assign the points for an
// individual command or override the input arguments.
type TargetCommand struct {
	Id     string   `yaml:"id" bson:"cmd_id"` // ID, must match ID in test file
	Index  int      `yaml:"index"`            // Index > 0 => match to index in test
	Points uint     `yaml:"points"`           // Points for this command
	Args   []string `yaml:"args"`             // Argument overrides
}

// TargetListItem is the target detail we send to remote clients about a target
type TargetListItem struct {
	Name        string
	PrintName   string
	Description string
	Active      string
	Type        string
	Version     uint
	Points      uint
	FileName    string
	FileHash    string
	CollabMsg   string
}

// TargetList is the JSON blob sent to clients
type TargetList struct {
	Targets []*TargetListItem
}

// NewTarget creates a new, empty Target with the default type of "asst"
func NewTarget() *Target {
	t := &Target{
		Type: TARGET_ASST,
	}
	return t
}

func (t *Target) fixDefaults() {
	// Ugly, but we need to merge defaults within inner structs
	for _, test := range t.Tests {
		if test.Scoring != TEST_SCORING_PARTIAL {
			test.Scoring = TEST_SCORING_ENTIRE
		}
	}

	if t.Active != "false" {
		t.Active = "true"
	}

	if t.Leaderboard != "false" {
		t.Leaderboard = "true"
	}
}

// TargetFromFile creates a Target object from a yaml file
func TargetFromFile(file string) (*Target, error) {
	var err error

	var info os.FileInfo
	if info, err = os.Stat(file); err != nil {
		return nil, err
	}

	data, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("Error reading file %v: %v", file, err)
	}

	if t, err := TargetFromString(string(data)); err != nil {
		return t, fmt.Errorf("Error loading target file %v: %v", file, err)
	} else {
		// Save file version and hash
		t.FileName = info.Name()
		raw := md5.Sum(data)
		t.FileHash = strings.ToLower(hex.EncodeToString(raw[:]))
		return t, nil
	}
}

// TargetFromString creates a Target object from a yaml string
func TargetFromString(text string) (*Target, error) {
	t := NewTarget()
	err := yaml.Unmarshal([]byte(text), t)

	if err != nil {
		return nil, err
	}

	t.fixDefaults()

	return t, nil
}

// Map the target test points onto the runnable test
func (tt *TargetTest) applyTo(test *Test) error {
	test.PointsAvailable = tt.Points
	test.ScoringMethod = tt.Scoring
	test.MemLeakPoints = tt.MemLeakPoints

	// We may need to apply arguments and points to each command. In the simplest
	// case, the Target doesn't override command behavior or points and maps all
	// points to the entire test ("entire" scoring). With this method of scoring,
	// all commands must complete successfully in order to gain any (and all)
	// points. In this case, we still need to apply the args, but that's it. For
	// partial scoring, points must be specified for each command that receives any.
	//
	// Before we do that, we need to be able to find the commands. Moreover, we
	// need to make sure the input is sane.  We allow a single instance of a
	// command to apply to multiple command instances, and also a per-instance
	// 1-1 mapping. For example, we may have a test that consists of:
	//		/testbin/forktest
	//		/testbin/forktest
	//		/testbin/forktest
	// If /testbin/forktest is specified once in the TargetTest, with no index,
	// then its point mapping applies to all 3 instances. But, one can also
	// specify different point values for each test.  In this case, we require
	// a 1-1 mapping.

	// Store a mapping of id -> list of command instances so we can (1) verify
	// all instances have been accounted for if indexes are specified and (2)
	// find the command instance to apply points and args to.
	type cmdData struct {
		command *Command
		done    bool
	}

	// id -> list of command instances (there could be more than 1)
	commandInstances := make(map[string][]*cmdData)

	for _, cmd := range test.Commands {
		id := cmd.Id()
		if _, ok := commandInstances[id]; !ok {
			commandInstances[id] = make([]*cmdData, 0)
		}
		commandInstances[id] = append(commandInstances[id], &cmdData{cmd, false})
	}

	// If partial scoring, this eventually needs to match the test points
	pointsAssigned := uint(0)

	// First pass - apply the arguments and command points if partial scoring.
	for _, cmd := range tt.Commands {
		instances, ok := commandInstances[cmd.Id]
		if !ok {
			return errors.New("Cannot find command instance: " + cmd.Id)
		}

		// This only applies to a certain index
		if cmd.Index > 0 {
			if cmd.Index > len(instances) {
				return errors.New("Invalid command index for " + cmd.Id)
			}
			instances = []*cmdData{instances[cmd.Index-1]}
		}

		for _, instance := range instances {
			if instance.done {
				return errors.New("Command instance already instantiated. Command: " + cmd.Id)
			} else {
				instance.done = true
				if len(cmd.Args) > 0 {
					instance.command.Input.replaceArgs(cmd.Args)
				}

				if tt.Scoring == TEST_SCORING_PARTIAL {
					instance.command.PointsAvailable = cmd.Points
					pointsAssigned += cmd.Points
				}
			}
		}
	}

	// Next, verify the following:
	//  1) Exactly all the points were assigned
	//	2) If indexes were specified, all instances were covered

	// (1)
	if tt.Scoring == TEST_SCORING_PARTIAL && pointsAssigned != tt.Points {
		return errors.New(fmt.Sprintf("Invalid partial command point assignment: available (%v) != assigned (%v)",
			tt.Points, pointsAssigned))
	}

	// (2) Verify all instances of specified commands are covered
	for _, cmd := range tt.Commands {
		instances := commandInstances[cmd.Id]
		for _, instance := range instances {
			if !instance.done {
				return errors.New("Unassigned command instance: " + cmd.Id)
			}
		}
		// We only need to check a command id once
		delete(commandInstances, cmd.Id)
	}

	return nil
}

// Initialize the target as a subtarget of a metatarget.
// This involves linking the target to its metatarget, and finding all of the
// subtargets that come before it in the same metatarget.
func (t *Target) initAsSubTarget(env *TestEnvironment) error {
	if len(t.MetaName) == 0 {
		return nil
	}

	metaTarget, ok := env.Targets[t.MetaName]
	if !ok {
		return errors.New("Cannot find the metatarget '" + t.MetaName + "'.")
	}

	foundThis := false
	others := make([]*Target, 0)
	for _, name := range metaTarget.SubTargetNames {
		if name == t.Name {
			foundThis = true
			// The subtargets must be in order, and since we've found
			// this one, we're done.
			break
		} else {
			if other, ok := env.Targets[name]; ok {
				others = append(others, other)
			} else {
				return fmt.Errorf("Cannot find subtarget '%v' in metatarget '%v'", name, metaTarget.Name)
			}
		}
	}

	if !foundThis {
		return fmt.Errorf("Cannot find main subtarget '%v' in metatarget '%v'", t.Name, metaTarget.Name)
	}

	// Link this target to its metatarget and younger subtarget siblings.
	t.metaTarget = metaTarget
	t.previousSubTargets = others

	return nil
}

func (t *Target) initAsMetaTarget(env *TestEnvironment) error {
	if !t.IsMetaTarget {
		return fmt.Errorf("Target '%v' is not a metatarget", t.Name)
	}

	if len(t.Tests) > 0 {
		return fmt.Errorf("MetaTargets cannot have additional tests. Metatarget: %v", t.Name)
	}

	if len(t.SubTargetNames) == 0 {
		return fmt.Errorf("MetaTargets must contain at least one subtarget. Metatarget: %v", t.Name)
	}

	points := uint(0)

	t.previousSubTargets = make([]*Target, 0, len(t.SubTargetNames))

	for _, subname := range t.SubTargetNames {
		subtarget, ok := env.Targets[subname]
		if !ok {
			return fmt.Errorf("Cannot find subtarget '%v'", subname)
		}
		points += subtarget.Points

		// These need to match
		if subtarget.RequiresUserland != t.RequiresUserland {
			return fmt.Errorf("Subtarget and metatarget must have the same userland requirement.")
		}
		if subtarget.KConfig != t.KConfig {
			return fmt.Errorf("Subtarget and metatarget must use the same kernel configuration.")
		}
		if subtarget.Type != t.Type {
			return fmt.Errorf("Subtarget and metatarget must have the same type.")
		}
		t.previousSubTargets = append(t.previousSubTargets, subtarget)
	}

	if points != t.Points {
		return fmt.Errorf("Metatarget points (%v) do not match total of subtarget points (%v)", t.Points, points)
	}

	return nil
}

// Recursively process the test dependencies, marking the test
// as required by the target.
func assignTestRequiredBy(test *Test, name string) {
	test.requiredBy[name] = true
	for _, dep := range test.ExpandedDeps {
		assignTestRequiredBy(dep, name)
	}
}

// For each graded test, figure out which tests are required.
// We use this information to split out the metasubmission into multiple
// submissions.
func assignRequiredBy(tg *TestGroup) {
	for _, test := range tg.Tests {
		if len(test.TargetName) > 0 {
			assignTestRequiredBy(test, test.TargetName)
		}
	}
}

// Instance creates a runnable TestGroup from this Target
func (t *Target) Instance(env *TestEnvironment) (*TestGroup, []error) {

	// Create a TestGroup with the tests from all of the targets we're running.
	allTargets := []*Target{}

	// We don't permit metatargets to contain extra tests, so ignore it.
	if !t.IsMetaTarget {
		allTargets = append(allTargets, t)
	}

	// This will be populated for subtargets and metatargets
	if len(t.previousSubTargets) > 0 {
		allTargets = append(allTargets, t.previousSubTargets...)
	}

	// First, create a group config and convert it to a TestGroup.
	config := &GroupConfig{
		Name:    t.Name,
		UseDeps: true,
		Env:     env,
		Tests:   make([]string, 0),
	}

	done := make(map[string]bool)

	for _, target := range allTargets {
		for _, tt := range target.Tests {
			if _, ok := done[tt.Id]; ok {
				return nil, []error{
					fmt.Errorf("Duplicate test detected: '%v'. Duplicate tests are not allowed in targets.", tt.Id),
				}
			}
			config.Tests = append(config.Tests, tt.Id)
			done[tt.Id] = true
		}
	}

	// Create one combined TestGroup with dependencies for all targets we're running
	group, errs := GroupFromConfig(config)
	if len(errs) > 0 {
		return nil, errs
	}

	// We have a runnable group with dependencies.  Next, we need
	// to assign points, scoring method, args, etc.
	for _, target := range allTargets {
		total := uint(0)
		for _, tt := range target.Tests {
			test, ok := group.Tests[tt.Id]

			if !ok {
				return nil, []error{errors.New("Cannot find " + tt.Id + " in the TestGroup")}
			}
			if err := tt.applyTo(test); err != nil {
				return nil, []error{err}
			}
			// This is used for scoring later
			test.TargetName = target.Name

			total += tt.Points
		}

		if total != target.Points {
			return nil, []error{fmt.Errorf("Target points (%v) do not match sum(test points) (%v)", target.Points, total)}
		}
	}

	assignRequiredBy(group)

	return group, nil
}

// Determine whether or not we'll allow the target to replaced in the DB. If we change
// things like the print name, active flag, etc. we should just update it in the DB.
// But, if we chahnge the tests or points, we should be creating a new version.
func (old *Target) isChangeAllowed(other *Target) error {

	if old.Version != other.Version {
		return errors.New("Mismatched target versions. isChangeAllowed only applies to targets with the same version number.")
	}

	// The ID will be different, and that's OK, as long as we update the right one in the DB.
	if old.Name != other.Name {
		return errors.New("Changing the target name requires a version change")
	}
	if old.Type != other.Type {
		return errors.New("Changing the target type requires a version change")
	}
	if old.Points != other.Points {
		return errors.New("Changing the target points requires a version change")
	}
	if old.IsMetaTarget != other.IsMetaTarget {
		return errors.New("Chaning the target is_meta_target flag requires a version change")
	}

	// TODO: Relying on no duplicate tests

	// We do care about the tests, just not the order
	if len(old.Tests) != len(other.Tests) {
		return errors.New("Changing the number of tests requiers a version change")
	}

	oldMap := make(map[string]*TargetTest)

	for _, t := range old.Tests {
		oldMap[t.Id] = t
	}

	for _, t := range other.Tests {
		if oldVer, ok := oldMap[t.Id]; !ok {
			return fmt.Errorf("Test %v was removed in the new target, which requires a version change", t.Id)
		} else if oldVer.Points != t.Points {
			return fmt.Errorf("The point distribution for %v changed in the new target, which requires a version change", t.Id)
		} else if oldVer.Scoring != t.Scoring {
			return fmt.Errorf("The scoring method for %v changed in the new target, which requires a version change", t.Id)
		} else if oldVer.MemLeakPoints != t.MemLeakPoints {
			return errors.New("The memory leak points for %v changed in the new target, which requires a version change")
		}
	}

	// Subtarget names
	if len(old.SubTargetNames) != len(other.SubTargetNames) {
		return errors.New("Changing the number of subtargets requiers a version change")
	}

	oldSubTargetMap := make(map[string]bool)

	for _, name := range old.SubTargetNames {
		oldSubTargetMap[name] = true
	}

	for _, name := range other.SubTargetNames {
		if !oldSubTargetMap[name] {
			return fmt.Errorf("Subtarget %v was removed in the new target, which requires a version change", name)
		}
	}

	// Fields we don't care about:
	//
	// PrintName, Description, Active, RequiredCommit, Link
	// KConfig is set based on the Name
	// RequiresUserland: if this was broken, tests would have failed
	// FileHash: this will change
	// FileName: OK if it moves

	return nil
}
