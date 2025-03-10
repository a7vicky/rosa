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

package oidcconfig

import (
	// nolint:gosec

	//#nosec GSC-G505 -- Import blacklist: crypto/sha1

	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	v1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/zgalor/weberr"
	"gopkg.in/square/go-jose.v2"

	"github.com/openshift/rosa/cmd/create/oidcprovider"
	"github.com/openshift/rosa/pkg/arguments"
	"github.com/openshift/rosa/pkg/aws"
	awscb "github.com/openshift/rosa/pkg/aws/commandbuilder"
	"github.com/openshift/rosa/pkg/aws/tags"
	"github.com/openshift/rosa/pkg/helper"
	"github.com/openshift/rosa/pkg/interactive"
	"github.com/openshift/rosa/pkg/interactive/confirm"
	"github.com/openshift/rosa/pkg/rosa"
)

var args struct {
	region           string
	rawFiles         bool
	userPrefix       string
	managed          bool
	installerRoleArn string
}

var Cmd = &cobra.Command{
	Use:     "oidc-config",
	Aliases: []string{"oidcconfig", "oidcconfig"},
	Short:   "Create OIDC config compliant with OIDC protocol.",
	Long: "Create OIDC config in a S3 bucket for the " +
		"client AWS account and populates it to be compliant with OIDC protocol. " +
		"It also creates a Secret in Secrets Manager containing the private key.",
	Example: `  # Create OIDC config
	rosa create oidc-config`,
	Run: run,
}

const (
	defaultLengthRandomLabel = 4
	maxLengthUserPrefix      = 15

	rawFilesFlag         = "raw-files"
	userPrefixFlag       = "prefix"
	managedFlag          = "managed"
	installerRoleArnFlag = "installer-role-arn"

	prefixForPrivateKeySecret     = "rosa-private-key"
	defaultPrefixForConfiguration = "oidc"
	minorVersionForGetSecret      = "4.12"
)

func init() {
	flags := Cmd.Flags()

	flags.BoolVar(
		&args.rawFiles,
		rawFilesFlag,
		false,
		"Creates OIDC config documents (Private RSA key, Discovery document, JSON Web Key Set) "+
			"and saves locally for the client to create the configuration.",
	)

	flags.StringVar(
		&args.userPrefix,
		userPrefixFlag,
		"",
		"Prefix for the OIDC configuration, secret and provider.",
	)

	flags.BoolVar(
		&args.managed,
		managedFlag,
		false,
		"Indicates whether it is a Red Hat managed or unmanaged (Customer hosted) OIDC Configuration.",
	)

	flags.StringVar(
		&args.installerRoleArn,
		installerRoleArnFlag,
		"",
		"STS Role ARN with get secrets permission.",
	)

	aws.AddModeFlag(Cmd)

	confirm.AddFlag(flags)
	interactive.AddFlag(flags)
	arguments.AddRegionFlag(flags)
}

func checkInteractiveModeNeeded(cmd *cobra.Command) {
	modeNotChanged := !cmd.Flags().Changed("mode")
	if modeNotChanged && !cmd.Flags().Changed(rawFilesFlag) {
		interactive.Enable()
		return
	}
	modeIsAuto := cmd.Flag("mode").Value.String() == aws.ModeAuto
	installerRoleArnNotSet := (!cmd.Flags().Changed(installerRoleArnFlag) || args.installerRoleArn == "") && !confirm.Yes()
	if !args.managed && (modeNotChanged || (modeIsAuto && installerRoleArnNotSet)) {
		interactive.Enable()
		return
	}
}

func run(cmd *cobra.Command, argv []string) {
	r := rosa.NewRuntime().WithAWS().WithOCM()
	defer r.Cleanup()

	mode, err := aws.GetMode()
	if err != nil {
		r.Reporter.Errorf("%s", err)
		os.Exit(1)
	}

	// Get AWS region
	region, err := aws.GetRegion(arguments.GetRegion())
	if err != nil {
		r.Reporter.Errorf("Error getting region: %v", err)
		os.Exit(1)
	}
	args.region = region

	checkInteractiveModeNeeded(cmd)

	if args.rawFiles && mode != "" {
		r.Reporter.Warnf("--%s param is not supported alongside --mode param.", rawFilesFlag)
		os.Exit(1)
	}

	if args.rawFiles && args.installerRoleArn != "" {
		r.Reporter.Warnf("--%s param is not supported alongside --%s param", rawFilesFlag, installerRoleArnFlag)
		os.Exit(1)
	}

	if args.rawFiles && args.managed {
		r.Reporter.Warnf("--%s param is not supported alongside --%s param", rawFilesFlag, managedFlag)
		os.Exit(1)
	}

	if !args.rawFiles && interactive.Enabled() && !cmd.Flags().Changed("mode") {
		question := "OIDC Config creation mode"
		if args.managed {
			r.Reporter.Warnf("For a managed OIDC Config only auto mode is supported. " +
				"However, you may choose the provider creation mode")
			question = "OIDC Provider creation mode"
		}
		mode, err = interactive.GetOption(interactive.Input{
			Question: question,
			Help:     cmd.Flags().Lookup("mode").Usage,
			Default:  aws.ModeAuto,
			Options:  aws.Modes,
			Required: true,
		})
		if err != nil {
			r.Reporter.Errorf("Expected a valid OIDC provider creation mode: %s", err)
			os.Exit(1)
		}
	}

	if args.managed && args.userPrefix != "" {
		r.Reporter.Warnf("--%s param is not supported for managed OIDC config", userPrefixFlag)
		os.Exit(1)
	}

	if args.managed && args.installerRoleArn != "" {
		r.Reporter.Warnf("--%s param is not supported for managed OIDC config", installerRoleArnFlag)
		os.Exit(1)
	}

	if !args.managed {
		if !args.rawFiles {
			r.Reporter.Infof("This command will create a S3 bucket populating it with documents " +
				"to be compliant with OIDC protocol. It will also create a Secret in Secrets Manager containing the private key")
			if mode == aws.ModeAuto && (interactive.Enabled() || confirm.Yes()) {
				args.installerRoleArn = interactive.GetInstallerRoleArn(r, cmd, args.installerRoleArn, minorVersionForGetSecret)
			}
			if interactive.Enabled() {
				prefix, err := interactive.GetString(interactive.Input{
					Question:   "Prefix for OIDC",
					Help:       cmd.Flags().Lookup(userPrefixFlag).Usage,
					Default:    args.userPrefix,
					Validators: []interactive.Validator{interactive.MaxLength(maxLengthUserPrefix)},
				})
				if err != nil {
					r.Reporter.Errorf("Expected a valid prefix for the configuration: %s", err)
					os.Exit(1)
				}
				args.userPrefix = prefix
			}
			err := aws.ARNValidator(args.installerRoleArn)
			if err != nil {
				r.Reporter.Errorf("Expected a valid ARN: %s", err)
				os.Exit(1)
			}
			roleName, _ := aws.GetResourceIdFromARN(args.installerRoleArn)
			if roleName != "" {
				roleExists, _, err := r.AWSClient.CheckRoleExists(roleName)
				if err != nil {
					r.Reporter.Errorf("There was a problem checking if role '%s' exists: %v", args.installerRoleArn, err)
					os.Exit(1)
				}
				if !roleExists {
					r.Reporter.Errorf("Role '%s' does not exist", args.installerRoleArn)
					os.Exit(1)
				}
			}
		}

		args.userPrefix = strings.Trim(args.userPrefix, " \t")

		if len([]rune(args.userPrefix)) > maxLengthUserPrefix {
			r.Reporter.Errorf("Expected a valid prefix for the configuration: "+
				"length of prefix is limited to %d characters", maxLengthUserPrefix)
			os.Exit(1)
		}
	}

	oidcConfigInput := buildOidcConfigInput(r)
	oidcConfigStrategy, err := getOidcConfigStrategy(mode, &oidcConfigInput)
	if err != nil {
		r.Reporter.Errorf("%s", err)
		os.Exit(1)
	}
	oidcConfigStrategy.execute(r)
	if !args.rawFiles {
		oidcprovider.Cmd.Run(oidcprovider.Cmd, []string{"", mode, oidcConfigInput.IssuerUrl})
	}
}

type OidcConfigInput struct {
	BucketName           string
	IssuerUrl            string
	PrivateKey           []byte
	PrivateKeyFilename   string
	DiscoveryDocument    string
	Jwks                 []byte
	PrivateKeySecretName string
}

const (
	bucketNameRegex = "^[a-z][a-z0-9\\-]+[a-z0-9]$"
)

func isValidBucketName(bucketName string) bool {
	if bucketName[0] == '.' || bucketName[len(bucketName)-1] == '.' {
		return false
	}
	if strings.HasPrefix(bucketName, "xn--") {
		return false
	}
	if strings.HasSuffix(bucketName, "-s3alias") {
		return false
	}
	if match, _ := regexp.MatchString("\\.\\.", bucketName); match {
		return false
	}
	// We don't support buckets with '.' in them
	match, _ := regexp.MatchString(bucketNameRegex, bucketName)
	return match
}

func buildOidcConfigInput(r *rosa.Runtime) OidcConfigInput {
	if args.managed {
		return OidcConfigInput{}
	}
	randomLabel := helper.RandomLabel(defaultLengthRandomLabel)
	bucketName := fmt.Sprintf("%s-%s", defaultPrefixForConfiguration, randomLabel)
	if args.userPrefix != "" {
		bucketName = fmt.Sprintf("%s-%s", args.userPrefix, bucketName)
	}
	if !isValidBucketName(bucketName) {
		r.Reporter.Errorf("The bucket name '%s' is not valid", bucketName)
		os.Exit(1)
	}
	privateKeySecretName := fmt.Sprintf("%s-%s", prefixForPrivateKeySecret, bucketName)
	bucketUrl := fmt.Sprintf("https://%s.s3.%s.amazonaws.com", bucketName, args.region)
	privateKey, publicKey, err := createKeyPair()
	if err != nil {
		r.Reporter.Errorf("There was a problem generating key pair: %s", err)
		os.Exit(1)
	}
	privateKeyFilename := fmt.Sprintf("%s.key", privateKeySecretName)
	discoveryDocument := generateDiscoveryDocument(bucketUrl)
	jwks, err := buildJSONWebKeySet(publicKey)
	if err != nil {
		r.Reporter.Errorf("There was a problem generating JSON Web Key Set: %s", err)
		os.Exit(1)
	}
	return OidcConfigInput{
		BucketName:           bucketName,
		IssuerUrl:            bucketUrl,
		PrivateKey:           privateKey,
		PrivateKeyFilename:   privateKeyFilename,
		DiscoveryDocument:    discoveryDocument,
		Jwks:                 jwks,
		PrivateKeySecretName: privateKeySecretName,
	}
}

type CreateOidcConfigStrategy interface {
	execute(r *rosa.Runtime)
}

type CreateUnmanagedOidcConfigRawStrategy struct {
	oidcConfig *OidcConfigInput
}

func (s *CreateUnmanagedOidcConfigRawStrategy) execute(r *rosa.Runtime) {
	bucketName := s.oidcConfig.BucketName
	discoveryDocument := s.oidcConfig.DiscoveryDocument
	jwks := s.oidcConfig.Jwks
	privateKey := s.oidcConfig.PrivateKey
	privateKeyFilename := s.oidcConfig.PrivateKeyFilename
	err := helper.SaveDocument(string(privateKey), privateKeyFilename)
	if err != nil {
		r.Reporter.Errorf("There was a problem saving private key to a file: %s", err)
		os.Exit(1)
	}
	discoveryDocumentFilename := fmt.Sprintf("discovery-document-%s.json", bucketName)
	err = helper.SaveDocument(discoveryDocument, discoveryDocumentFilename)
	if err != nil {
		r.Reporter.Errorf("There was a problem saving discovery document to a file: %s", err)
		os.Exit(1)
	}
	jwksFilename := fmt.Sprintf("jwks-%s.json", bucketName)
	err = helper.SaveDocument(string(jwks[:]), jwksFilename)
	if err != nil {
		r.Reporter.Errorf("There was a problem saving JSON Web Key Set to a file: %s", err)
		os.Exit(1)
	}
	if r.Reporter.IsTerminal() {
		r.Reporter.Infof("Please refer to documentation to use generated files to create an OIDC compliant configuration.")
	}
}

type CreateUnmanagedOidcConfigAutoStrategy struct {
	oidcConfig *OidcConfigInput
}

const (
	discoveryDocumentKey = ".well-known/openid-configuration"
	jwksKey              = "keys.json"
)

func (s *CreateUnmanagedOidcConfigAutoStrategy) execute(r *rosa.Runtime) {
	bucketUrl := s.oidcConfig.IssuerUrl
	bucketName := s.oidcConfig.BucketName
	discoveryDocument := s.oidcConfig.DiscoveryDocument
	jwks := s.oidcConfig.Jwks
	privateKey := s.oidcConfig.PrivateKey
	privateKeySecretName := s.oidcConfig.PrivateKeySecretName
	installerRoleArn := args.installerRoleArn
	var spin *spinner.Spinner
	if r.Reporter.IsTerminal() {
		spin = spinner.New(spinner.CharSets[9], 100*time.Millisecond)
		r.Reporter.Infof("Setting up unmanaged OIDC configuration '%s'", bucketName)
	}
	if spin != nil {
		spin.Start()
	}
	err := r.AWSClient.CreateS3Bucket(bucketName, args.region)
	if err != nil {
		r.Reporter.Errorf("There was a problem creating S3 bucket '%s': %s", bucketName, err)
		os.Exit(1)
	}
	err = r.AWSClient.PutPublicReadObjectInS3Bucket(
		bucketName, strings.NewReader(discoveryDocument), discoveryDocumentKey)
	if err != nil {
		r.Reporter.Errorf("There was a problem populating discovery "+
			"document to S3 bucket '%s': %s", bucketName, err)
		os.Exit(1)
	}
	err = r.AWSClient.PutPublicReadObjectInS3Bucket(bucketName, bytes.NewReader(jwks), jwksKey)
	if err != nil {
		if spin != nil {
			spin.Stop()
		}
		r.Reporter.Errorf("There was a problem populating JWKS "+
			"to S3 bucket '%s': %s", bucketName, err)
		os.Exit(1)
	}
	secretARN, err := r.AWSClient.CreateSecretInSecretsManager(privateKeySecretName, string(privateKey[:]))
	if err != nil {
		r.Reporter.Errorf("There was a problem saving private key to secrets manager: %s", err)
		os.Exit(1)
	}
	oidcConfig, err := v1.NewOidcConfig().
		Managed(false).
		SecretArn(secretARN).
		IssuerUrl(bucketUrl).
		InstallerRoleArn(installerRoleArn).
		Build()
	if err == nil {
		oidcConfig, err = r.OCMClient.CreateOidcConfig(oidcConfig)
	}
	if err != nil {
		if spin != nil {
			spin.Stop()
		}
		r.Reporter.Errorf("There was a problem building your unmanaged OIDC Configuration "+
			"with OCM: %v.\nPlease refer to documentation and try again through OCM CLI.", err)
		r.Reporter.Warnf("Secret ARN: %s\tBucketUrl: %s", secretARN, bucketUrl)
		os.Exit(1)
	}
	if r.Reporter.IsTerminal() {
		if spin != nil {
			spin.Stop()
		}
		output := "Please run the following command to create a cluster with this oidc config"
		output = fmt.Sprintf("%s\nrosa create cluster --sts --oidc-config-id %s", output, oidcConfig.ID())
		r.Reporter.Infof(output)
	}
}

type CreateUnmanagedOidcConfigManualStrategy struct {
	oidcConfig *OidcConfigInput
}

func (s *CreateUnmanagedOidcConfigManualStrategy) execute(r *rosa.Runtime) {
	commands := []string{}
	bucketName := s.oidcConfig.BucketName
	discoveryDocument := s.oidcConfig.DiscoveryDocument
	jwks := s.oidcConfig.Jwks
	privateKey := s.oidcConfig.PrivateKey
	privateKeyFilename := s.oidcConfig.PrivateKeyFilename
	privateKeySecretName := s.oidcConfig.PrivateKeySecretName
	err := helper.SaveDocument(string(privateKey), privateKeyFilename)
	if err != nil {
		r.Reporter.Errorf("There was a problem saving private key to a file: %s", err)
		os.Exit(1)
	}
	createBucketConfig := ""
	if args.region != aws.DefaultRegion {
		createBucketConfig = fmt.Sprintf("LocationConstraint=%s", args.region)
	}
	createS3BucketCommand := awscb.NewS3ApiCommandBuilder().
		SetCommand(awscb.CreateBucket).
		AddParam(awscb.Bucket, bucketName).
		AddParam(awscb.CreateBucketConfiguration, createBucketConfig).
		AddParam(awscb.Region, args.region).
		Build()
	commands = append(commands, createS3BucketCommand)

	putBucketTaggingCommand := awscb.NewS3ApiCommandBuilder().
		SetCommand(awscb.PutBucketTagging).
		AddParam(awscb.Bucket, bucketName).
		AddParam(awscb.Tagging, fmt.Sprintf("'TagSet=[{Key=%s,Value=%s}]'", tags.RedHatManaged, tags.True)).
		Build()
	commands = append(commands, putBucketTaggingCommand)

	discoveryDocumentFilename := fmt.Sprintf("discovery-document-%s.json", bucketName)
	err = helper.SaveDocument(discoveryDocument, discoveryDocumentFilename)
	if err != nil {
		r.Reporter.Errorf("There was a problem saving discovery document to a file: %s", err)
		os.Exit(1)
	}
	putDiscoveryDocumentCommand := awscb.NewS3ApiCommandBuilder().
		SetCommand(awscb.PutObject).
		AddParam(awscb.Acl, aws.AclPublicRead).
		AddParam(awscb.Body, fmt.Sprintf("./%s", discoveryDocumentFilename)).
		AddParam(awscb.Bucket, bucketName).
		AddParam(awscb.Key, discoveryDocumentKey).
		AddParam(awscb.Tagging, fmt.Sprintf("'%s=%s'", tags.RedHatManaged, tags.True)).
		Build()
	commands = append(commands, putDiscoveryDocumentCommand)
	commands = append(commands, fmt.Sprintf("rm %s", discoveryDocumentFilename))
	jwksFilename := fmt.Sprintf("jwks-%s.json", bucketName)
	err = helper.SaveDocument(string(jwks[:]), jwksFilename)
	if err != nil {
		r.Reporter.Errorf("There was a problem saving JSON Web Key Set to a file: %s", err)
		os.Exit(1)
	}
	putJwksCommand := awscb.NewS3ApiCommandBuilder().
		SetCommand(awscb.PutObject).
		AddParam(awscb.Acl, aws.AclPublicRead).
		AddParam(awscb.Body, fmt.Sprintf("./%s", jwksFilename)).
		AddParam(awscb.Bucket, bucketName).
		AddParam(awscb.Key, jwksKey).
		AddParam(awscb.Tagging, fmt.Sprintf("'%s=%s'", tags.RedHatManaged, tags.True)).
		Build()
	commands = append(commands, putJwksCommand)
	commands = append(commands, fmt.Sprintf("rm %s", jwksFilename))
	createSecretCommand := awscb.NewSecretsManagerCommandBuilder().
		SetCommand(awscb.CreateSecret).
		AddParam(awscb.Name, privateKeySecretName).
		AddParam(awscb.SecretString, fmt.Sprintf("file://%s", privateKeyFilename)).
		AddParam(awscb.Description, fmt.Sprintf("\"Secret for %s\"", bucketName)).
		AddParam(awscb.Region, args.region).
		AddTags(map[string]string{
			tags.RedHatManaged: "true",
		}).
		Build()
	commands = append(commands, createSecretCommand)
	commands = append(commands, fmt.Sprintf("rm %s", privateKeyFilename))
	fmt.Println(awscb.JoinCommands(commands))
	if r.Reporter.IsTerminal() {
		r.Reporter.Infof("Please run commands above to generate OIDC compliant configuration in your AWS account. " +
			"After running the commands please refer to the documentation to register your unmanaged OIDC Configuration " +
			"with OCM.")
	}
}

type CreateManagedOidcConfigAutoStrategy struct {
	oidcConfigInput *OidcConfigInput
}

func (s *CreateManagedOidcConfigAutoStrategy) execute(r *rosa.Runtime) {
	var spin *spinner.Spinner
	if r.Reporter.IsTerminal() {
		spin = spinner.New(spinner.CharSets[9], 100*time.Millisecond)
		r.Reporter.Infof("Setting up managed OIDC configuration")
	}
	if spin != nil {
		spin.Start()
	}
	oidcConfig, err := v1.NewOidcConfig().Managed(true).Build()
	if err != nil {
		r.Reporter.Errorf("There was a problem building the managed OIDC Configuration: %v", err)
		os.Exit(1)
	}
	oidcConfig, err = r.OCMClient.CreateOidcConfig(oidcConfig)
	if err != nil {
		if spin != nil {
			spin.Stop()
		}
		r.Reporter.Errorf("There was a problem registering your managed OIDC Configuration: %v", err)
		os.Exit(1)
	}
	if r.Reporter.IsTerminal() {
		if spin != nil {
			spin.Stop()
		}
		output := "Please run the following command to create a cluster with this oidc config"
		output = fmt.Sprintf("%s\nrosa create cluster --sts --oidc-config-id %s", output, oidcConfig.ID())
		r.Reporter.Infof(output)
	}
	s.oidcConfigInput.IssuerUrl = oidcConfig.IssuerUrl()
}

func getOidcConfigStrategy(mode string, input *OidcConfigInput) (CreateOidcConfigStrategy, error) {
	if args.rawFiles {
		return &CreateUnmanagedOidcConfigRawStrategy{oidcConfig: input}, nil
	}
	if args.managed {
		return &CreateManagedOidcConfigAutoStrategy{oidcConfigInput: input}, nil
	}
	switch mode {
	case aws.ModeAuto:
		return &CreateUnmanagedOidcConfigAutoStrategy{oidcConfig: input}, nil
	case aws.ModeManual:
		return &CreateUnmanagedOidcConfigManualStrategy{oidcConfig: input}, nil
	default:
		return nil, weberr.Errorf("Invalid mode. Allowed values are %s", aws.Modes)
	}
}

func createKeyPair() ([]byte, []byte, error) {
	bitSize := 4096

	// Generate RSA keypair
	privateKey, err := rsa.GenerateKey(rand.Reader, bitSize)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to generate private key")
	}
	encodedPrivateKey := pem.EncodeToMemory(&pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   x509.MarshalPKCS1PrivateKey(privateKey),
	})

	// Generate public key from private keypair
	pubKeyBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to generate public key from private")
	}
	encodedPublicKey := pem.EncodeToMemory(&pem.Block{
		Type:    "PUBLIC KEY",
		Headers: nil,
		Bytes:   pubKeyBytes,
	})

	return encodedPrivateKey, encodedPublicKey, nil
}

type JSONWebKeySet struct {
	Keys []jose.JSONWebKey `json:"keys"`
}

// buildJSONWebKeySet builds JSON web key set from the public key
func buildJSONWebKeySet(publicKeyContent []byte) ([]byte, error) {
	block, _ := pem.Decode(publicKeyContent)
	if block == nil {
		return nil, errors.Errorf("Failed to decode PEM file")
	}

	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to parse key content")
	}

	var alg jose.SignatureAlgorithm
	switch publicKey.(type) {
	case *rsa.PublicKey:
		alg = jose.RS256
	default:
		return nil, errors.Errorf("Public key is not of type RSA")
	}

	kid, err := keyIDFromPublicKey(publicKey)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to fetch key ID from public key")
	}

	var keys []jose.JSONWebKey
	keys = append(keys, jose.JSONWebKey{
		Key:       publicKey,
		KeyID:     kid,
		Algorithm: string(alg),
		Use:       "sig",
	})

	keySet, err := json.MarshalIndent(JSONWebKeySet{Keys: keys}, "", "    ")
	if err != nil {
		return nil, errors.Wrapf(err, "JSON encoding of web key set failed")
	}

	return keySet, nil
}

// keyIDFromPublicKey derives a key ID non-reversibly from a public key
// reference: https://github.com/kubernetes/kubernetes/blob/v1.21.0/pkg/serviceaccount/jwt.go#L89-L111
func keyIDFromPublicKey(publicKey interface{}) (string, error) {
	publicKeyDERBytes, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", errors.Wrapf(err, "Failed to serialize public key to DER format")
	}

	hasher := crypto.SHA256.New()
	hasher.Write(publicKeyDERBytes)
	publicKeyDERHash := hasher.Sum(nil)

	keyID := base64.RawURLEncoding.EncodeToString(publicKeyDERHash)

	return keyID, nil
}

const (
	discoveryDocumentTemplate = `{
	"issuer": "%s",
	"jwks_uri": "%s/keys.json",
	"response_types_supported": [
		"id_token"
	],
	"subject_types_supported": [
		"public"
	],
	"id_token_signing_alg_values_supported": [
		"RS256"
	],
	"claims_supported": [
		"aud",
		"exp",
		"sub",
		"iat",
		"iss",
		"sub"
	]
}`
)

func generateDiscoveryDocument(bucketURL string) string {
	return fmt.Sprintf(discoveryDocumentTemplate, bucketURL, bucketURL)
}
