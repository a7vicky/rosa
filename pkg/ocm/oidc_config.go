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

package ocm

import (
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
)

func (c *Client) GetOidcConfig(id string) (*cmv1.OidcConfig, error) {
	response, err := c.ocm.ClustersMgmt().V1().
		OidcConfigs().OidcConfig(id).Get().
		Send()
	if err != nil {
		return nil, handleErr(response.Error(), err)
	}

	return response.Body(), nil
}

func (c *Client) ListOidcConfigs() ([]*cmv1.OidcConfig, error) {
	response, err := c.ocm.ClustersMgmt().V1().
		OidcConfigs().
		List().Page(1).Size(-1).
		Send()
	if err != nil {
		return nil, handleErr(response.Error(), err)
	}

	return response.Items().Slice(), nil
}

func (c *Client) CreateOidcConfig(oidcConfig *cmv1.OidcConfig) (*cmv1.OidcConfig, error) {
	response, err := c.ocm.ClustersMgmt().V1().
		OidcConfigs().
		Add().Body(oidcConfig).
		Send()
	if err != nil {
		return nil, handleErr(response.Error(), err)
	}
	return response.Body(), nil
}

func (c *Client) DeleteOidcConfig(id string) error {
	response, err := c.ocm.ClustersMgmt().V1().
		OidcConfigs().OidcConfig(id).
		Delete().
		Send()
	if err != nil {
		return handleErr(response.Error(), err)
	}
	return nil
}
