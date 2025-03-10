/*
Copyright (c) 2023 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package operatorroles

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/briandowns/spinner"
	"github.com/spf13/cobra"

	"github.com/openshift/rosa/pkg/interactive"
	"github.com/openshift/rosa/pkg/interactive/confirm"
	"github.com/openshift/rosa/pkg/ocm"
	"github.com/openshift/rosa/pkg/output"
	"github.com/openshift/rosa/pkg/rosa"
)

var args struct {
	version string
	prefix  string
}

var Cmd = &cobra.Command{
	Use:     "operator-roles",
	Aliases: []string{"operatorrole", "operator-role", "operatorroles"},
	Short:   "List operator roles and policies",
	Long:    "List operator roles and policies for the current AWS account.",
	Example: `  # List all operator roles
  rosa list operator-roles`,
	Run: run,
}

const (
	versionFlag = "version"
	prefixFlag  = "prefix"
)

func init() {
	flags := Cmd.Flags()
	flags.SortFlags = false
	flags.StringVar(
		&args.version,
		versionFlag,
		"",
		"List only operator-roles that are associated with the given version.",
	)
	flags.StringVar(
		&args.prefix,
		prefixFlag,
		"",
		"List only operator-roles that are associated with the given prefix."+
			" The prefix must match up to openshift|kube-system",
	)
	output.AddFlag(Cmd)
}

func run(cmd *cobra.Command, _ []string) {
	r := rosa.NewRuntime().WithAWS().WithOCM()
	defer r.Cleanup()

	versionList, err := ocm.GetVersionMinorList(r.OCMClient)
	if err != nil {
		r.Reporter.Errorf("%s", err)
		os.Exit(1)
	}

	_, err = r.OCMClient.ValidateVersion(args.version, versionList,
		r.Cluster.Version().ChannelGroup(), r.Cluster.AWS().STS().RoleARN() == "", r.Cluster.Hypershift().Enabled())
	if err != nil {
		r.Reporter.Errorf("Version '%s' is invalid", args.version)
		os.Exit(1)
	}

	var spin *spinner.Spinner
	if r.Reporter.IsTerminal() {
		spin = spinner.New(spinner.CharSets[9], 100*time.Millisecond)
	}
	if spin != nil {
		r.Reporter.Infof("Fetching operator roles")
		spin.Start()
	}

	operatorsMap, err := r.AWSClient.ListOperatorRoles(args.version)

	if spin != nil {
		spin.Stop()
	}

	if err != nil {
		r.Reporter.Errorf("Failed to get operator roles: %v", err)
		os.Exit(1)
	}

	if len(operatorsMap) == 0 {
		r.Reporter.Infof("No operator roles available")
		os.Exit(0)
	}
	if output.HasFlag() {
		err = output.Print(operatorsMap)
		if err != nil {
			r.Reporter.Errorf("%s", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Create the writer that will be used to print the tabulated results:
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if args.prefix == "" {
		fmt.Fprintf(writer, "ROLE PREFIX\tAMOUNT IN BUNDLE\n")
		keys := []string{}
		for key, operatorRoleList := range operatorsMap {
			keys = append(keys, key)
			fmt.Fprintf(
				writer,
				"%s\t%d\n",
				key,
				len(operatorRoleList),
			)
		}
		writer.Flush()
		confirm.Prompt(true, "Would you like to detail a specific prefix")
		if !confirm.Yes() {
			os.Exit(0)
		}
		args.prefix, err = interactive.GetOption(interactive.Input{
			Question: "Operator Role Prefix",
			Help:     cmd.Flags().Lookup("prefix").Usage,
			Options:  keys,
			Default:  keys[0],
			Required: true,
		})
		if err != nil {
			r.Reporter.Errorf("Expected a valid OIDC Config ID: %s", err)
			os.Exit(1)
		}
	}
	if args.prefix != "" {
		fmt.Fprintf(writer, "ROLE NAME\tROLE ARN\tVERSION\tMANAGED\n")
		for _, operatorRole := range operatorsMap[args.prefix] {
			awsManaged := "No"
			if operatorRole.ManagedPolicy {
				awsManaged = "Yes"
			}
			fmt.Fprintf(
				writer,
				"%s\t%s\t%s\t%s\n",
				operatorRole.RoleName,
				operatorRole.RoleARN,
				operatorRole.Version,
				awsManaged,
			)
		}
		writer.Flush()
	}
}
