/*
Copyright (c) 2020 Red Hat, Inc.

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

package create

import (
	"github.com/spf13/cobra"

	"github.com/openshift/rosa/cmd/create/accountroles"
	"github.com/openshift/rosa/cmd/create/admin"
	"github.com/openshift/rosa/cmd/create/cluster"
	"github.com/openshift/rosa/cmd/create/idp"
	"github.com/openshift/rosa/cmd/create/ingress"
	"github.com/openshift/rosa/cmd/create/machinepool"
	"github.com/openshift/rosa/cmd/create/ocmrole"
	"github.com/openshift/rosa/cmd/create/oidcconfig"
	"github.com/openshift/rosa/cmd/create/oidcprovider"
	"github.com/openshift/rosa/cmd/create/operatorroles"
	"github.com/openshift/rosa/cmd/create/service"
	"github.com/openshift/rosa/cmd/create/userrole"

	"github.com/openshift/rosa/pkg/arguments"
	"github.com/openshift/rosa/pkg/interactive/confirm"
)

var Cmd = &cobra.Command{
	Use:     "create",
	Aliases: []string{"add"},
	Short:   "Create a resource from stdin",
	Long:    "Create a resource from stdin",
}

func init() {
	Cmd.AddCommand(accountroles.Cmd)
	Cmd.AddCommand(admin.Cmd)
	Cmd.AddCommand(cluster.Cmd)
	Cmd.AddCommand(idp.Cmd)
	Cmd.AddCommand(ingress.Cmd)
	Cmd.AddCommand(machinepool.Cmd)
	Cmd.AddCommand(oidcconfig.Cmd)
	Cmd.AddCommand(oidcprovider.Cmd)
	Cmd.AddCommand(operatorroles.Cmd)
	Cmd.AddCommand(userrole.Cmd)
	Cmd.AddCommand(ocmrole.Cmd)
	Cmd.AddCommand(service.Cmd)

	flags := Cmd.PersistentFlags()
	arguments.AddProfileFlag(flags)
	arguments.AddRegionFlag(flags)
	confirm.AddFlag(flags)

	globallyAvailableCommands := []*cobra.Command{
		accountroles.Cmd, operatorroles.Cmd,
		userrole.Cmd, ocmrole.Cmd,
		oidcprovider.Cmd,
	}
	arguments.MarkRegionHidden(Cmd, globallyAvailableCommands)
}
