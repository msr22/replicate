package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/logrusorgru/aurora"
	"github.com/spf13/cobra"

	"replicate.ai/cli/pkg/config"
	"replicate.ai/cli/pkg/console"
	"replicate.ai/cli/pkg/project"
	"replicate.ai/cli/pkg/storage"
)

var timezone = time.Local

func newShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <experiment-or-commit-id>",
		Short: "View information about an experiment or commit",
		RunE:  show,
		Args:  cobra.ExactArgs(1),
	}

	// TODO(andreas): support json output
	addStorageURLFlag(cmd)

	return cmd
}

func show(cmd *cobra.Command, args []string) error {
	prefix := args[0]
	storageURL, _, err := getStorageURLFromFlagOrConfig(cmd)
	if err != nil {
		return err
	}
	store, err := storage.ForURL(storageURL)
	if err != nil {
		return err
	}
	if storage.NeedsCaching(store) {
		console.Info("Fetching data from %q...", store.RootURL())
	}
	proj := project.NewProject(store)
	result, err := proj.CommitOrExperimentFromPrefix(prefix)
	if err != nil {
		return err
	}

	au := getAurora()

	if result.Commit != nil {
		return showCommit(au, os.Stdout, proj, result.Commit)
	}
	return showExperiment(au, os.Stdout, proj, result.Experiment)
}

func showCommit(au aurora.Aurora, out io.Writer, proj *project.Project, com *project.Commit) error {
	exp, err := proj.ExperimentByID(com.ExperimentID)
	if err != nil {
		return err
	}
	experimentRunning, err := proj.ExperimentIsRunning(exp.ID)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "%s\n\n", au.Underline(au.Bold((fmt.Sprintf("Commit: %s", com.ID)))))

	w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	fmt.Fprintf(w, "Created:\t%s\n", com.Created.In(timezone).Format(time.RFC1123))
	fmt.Fprintf(w, "Path:\t%s\n", com.Path)
	fmt.Fprintf(w, "Step:\t%d\n", com.Step)

	fmt.Fprintf(w, "\t\n")
	fmt.Fprintf(w, "%s\t\n", au.Bold("Experiment"))

	fmt.Fprintf(w, "ID:\t%s\n", exp.ID)

	writeExperimentCommon(au, w, exp, experimentRunning)

	if err := writeCommitMetrics(au, w, proj, com); err != nil {
		return err
	}

	fmt.Fprintln(w)
	return w.Flush()
}

func showExperiment(au aurora.Aurora, out io.Writer, proj *project.Project, exp *project.Experiment) error {
	experimentRunning, err := proj.ExperimentIsRunning(exp.ID)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "%s\n\n", au.Underline(au.Bold(fmt.Sprintf("Experiment: %s", exp.ID))))

	w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	writeExperimentCommon(au, w, exp, experimentRunning)
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(out, "%s\n", au.Bold("Commits"))

	commits, err := proj.ExperimentCommits(exp.ID)
	if err != nil {
		return err
	}
	bestCommit, err := proj.ExperimentBestCommit(exp.ID)
	if err != nil {
		return err
	}
	var primaryMetric *config.Metric
	if exp.Config != nil && exp.Config.PrimaryMetric() != nil {
		primaryMetric = exp.Config.PrimaryMetric()
	}
	labelNames := []string{}

	cw := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	headings := []string{"ID", "STEP", "CREATED"}
	// FIXME(bfirsh): labels might change during experiment
	if len(commits) != 0 {
		for label := range commits[0].Labels {
			labelNames = append(labelNames, label)
		}
		// TODO: put primary first
		sort.Strings(labelNames)
		for _, label := range labelNames {
			headings = append(headings, strings.ToUpper(label))
		}
	}
	fmt.Fprintf(cw, "%s\n", strings.Join(headings, "\t"))

	for _, commit := range commits {
		columns := []string{commit.ShortID(), strconv.Itoa(commit.Step), console.FormatTime(commit.Created)}
		for _, label := range labelNames {
			val := commit.Labels[label]
			s := val.ShortString(10, 5)
			if bestCommit != nil && bestCommit.ID == commit.ID && primaryMetric != nil && primaryMetric.Name == label {
				// TODO (bfirsh): this could be done more elegantly with some formatting
				s += " (best)"
			}
			columns = append(columns, s)
		}
		fmt.Fprintf(cw, "%s\n", strings.Join(columns, "\t"))
	}
	if err := cw.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(out, "\n")
	fmt.Fprintf(out, "To see more details about a commit, run:\n")
	fmt.Fprintf(out, "  replicate show COMMIT_ID\n")
	return nil
}

func writeExperimentCommon(au aurora.Aurora, w *tabwriter.Writer, exp *project.Experiment, experimentRunning bool) {
	fmt.Fprintf(w, "Created:\t%s\n", exp.Created.In(timezone).Format(time.RFC1123))
	if experimentRunning {
		fmt.Fprint(w, "Status:\trunning\n")
	} else {
		fmt.Fprint(w, "Status:\tstopped\n")
	}
	fmt.Fprintf(w, "Host:\t%s\n", exp.Host)
	fmt.Fprintf(w, "User:\t%s\n", exp.User)
	fmt.Fprintf(w, "Command:\t%s\n", exp.Command)

	fmt.Fprintf(w, "\t\n")
	fmt.Fprintf(w, "%s\t\n", au.Bold("Params"))

	if len(exp.Params) > 0 {
		for _, p := range exp.SortedParams() {
			fmt.Fprintf(w, "%s:\t%s\n", p.Name, p.Value.String())
		}
	} else {
		fmt.Fprintf(w, "%s\t\n", au.Faint("(none)"))
	}

	fmt.Fprintf(w, "\t\n")
}

func writeCommitMetrics(au aurora.Aurora, w *tabwriter.Writer, proj *project.Project, com *project.Commit) error {
	exp, err := proj.ExperimentByID(com.ExperimentID)
	if err != nil {
		return err
	}
	conf := exp.Config
	if conf == nil {
		conf = new(config.Config)
	}

	metricNameSet := map[string]bool{}

	if len(conf.Metrics) > 0 {
		fmt.Fprintf(w, "%s\t\n", au.Bold("Metrics"))

		for _, metric := range conf.Metrics {
			valueString := "(not set)"
			value, ok := com.Labels[metric.Name]
			if ok {
				valueString = value.String()
			}
			primaryString := ""
			if metric.Primary {
				primaryString = "primary, "
			}
			fmt.Fprintf(w, "%s:\t%s (%sgoal: %s)\n", metric.Name, valueString, primaryString, metric.Goal)
			metricNameSet[metric.Name] = true
		}

		fmt.Fprintf(w, "\t\n")
	}
	labelNames := []string{}
	for name := range com.Labels {
		if _, ok := metricNameSet[name]; !ok {
			labelNames = append(labelNames, name)
		}
	}
	if len(labelNames) > 0 {
		fmt.Fprintf(w, "%s\t\n", au.Bold("Labels"))
		for _, lab := range com.SortedLabels() {
			if _, ok := metricNameSet[lab.Name]; !ok {
				fmt.Fprintf(w, "%s:\t%s\n", lab.Name, lab.Value.String())
			}
		}
	}
	return nil
}
