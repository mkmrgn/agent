package clicommand

import (
	"strings"
	"fmt"
	agent "github.com/buildkite/agent/v3/agent"
	"github.com/buildkite/agent/v3/api"
	"github.com/buildkite/agent/v3/cliconfig"
	"github.com/urfave/cli"
)

var UploadHelpDescription = `Usage:

   buildkite-agent artifact upload [options] <pattern> [destination]

Description:

   Uploads files to a job as artifacts.

   You need to ensure that the paths are surrounded by quotes otherwise the
   built-in shell path globbing will provide the files, which is currently not
   supported.

   You can specify an alternate destination on Amazon S3, Google Cloud Storage
   or Artifactory as per the examples below. This may be specified in the
   'destination' argument, or in the 'BUILDKITE_ARTIFACT_UPLOAD_DESTINATION'
   environment variable.  Otherwise, artifacts are uploaded to a
   Buildkite-managed Amazon S3 bucket, where they’re retained for six months.

Example:

   $ buildkite-agent artifact upload "log/**/*.log"

   You can also upload directly to Amazon S3 if you'd like to host your own artifacts:

   $ export BUILDKITE_S3_ACCESS_KEY_ID=xxx
   $ export BUILDKITE_S3_SECRET_ACCESS_KEY=yyy
   $ export BUILDKITE_S3_DEFAULT_REGION=eu-central-1 # default is us-east-1
   $ export BUILDKITE_S3_ACL=private # default is public-read
   $ buildkite-agent artifact upload "log/**/*.log" s3://name-of-your-s3-bucket/$BUILDKITE_JOB_ID

   You can use Amazon IAM assumed roles by specifying the session token:

   $ export BUILDKITE_S3_SESSION_TOKEN=zzz

   Or upload directly to Google Cloud Storage:

   $ export BUILDKITE_GS_ACL=private
   $ buildkite-agent artifact upload "log/**/*.log" gs://name-of-your-gs-bucket/$BUILDKITE_JOB_ID

   Or upload directly to Artifactory:

   $ export BUILDKITE_ARTIFACTORY_URL=http://my-artifactory-instance.com/artifactory
   $ export BUILDKITE_ARTIFACTORY_USER=carol-danvers
   $ export BUILDKITE_ARTIFACTORY_PASSWORD=xxx
   $ buildkite-agent artifact upload "log/**/*.log" rt://name-of-your-artifactory-repo/$BUILDKITE_JOB_ID`

var FollowSymlinksFlag = cli.BoolFlag{
	Name:   "follow-symlinks",
	Usage:  "Follow symbolic links while resolving globs",
	EnvVar: "BUILDKITE_AGENT_ARTIFACT_SYMLINKS",
}

type ArtifactUploadConfig struct {
	UploadPaths string `cli:"arg:0" label:"upload paths" validate:"required"`
	Destination string `cli:"arg:1" label:"destination" env:"BUILDKITE_ARTIFACT_UPLOAD_DESTINATION"`
	Job         string `cli:"job" validate:"required"`
	ContentType string `cli:"content-type"`
	S3ACL 		string `cli:"s3-acl" env:"BUILDKITE_S3_ACL"`

	// Global flags
	Debug       bool     `cli:"debug"`
	NoColor     bool     `cli:"no-color"`
	Experiments []string `cli:"experiment" normalize:"list"`
	Profile     string   `cli:"profile"`

	// API config
	DebugHTTP        bool   `cli:"debug-http"`
	AgentAccessToken string `cli:"agent-access-token" validate:"required"`
	Endpoint         string `cli:"endpoint" validate:"required"`
	NoHTTP2          bool   `cli:"no-http2"`

	// Uploader flags
	FollowSymlinks bool `cli:"follow-symlinks"`
}

var ArtifactUploadCommand = cli.Command{
	Name:        "upload",
	Usage:       "Uploads files to a job as artifacts",
	Description: UploadHelpDescription,
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:   "job",
			Value:  "",
			Usage:  "Which job should the artifacts be uploaded to",
			EnvVar: "BUILDKITE_JOB_ID",
		},
		cli.StringFlag{
			Name:   "content-type",
			Value:  "",
			Usage:  "A specific Content-Type to set for the artifacts (otherwise detected)",
			EnvVar: "BUILDKITE_ARTIFACT_CONTENT_TYPE",
		},
		cli.StringFlag {
			Name: "s3-acl",
			Value: "",
			Usage: "Set the ACL for objects uploaded to S3 (defaults to public-read)",
			EnvVar: "BUILDKITE_S3_ACL",
		},

		// API Flags
		AgentAccessTokenFlag,
		EndpointFlag,
		NoHTTP2Flag,
		DebugHTTPFlag,

		// Global flags
		NoColorFlag,
		DebugFlag,
		ExperimentsFlag,
		ProfileFlag,
		FollowSymlinksFlag,
	},
	Action: func(c *cli.Context) {
		var err error
		// The configuration will be loaded into this struct
		cfg := ArtifactUploadConfig{}

		l := CreateLogger(&cfg)

		// Load the configuration
		if err = cliconfig.Load(c, l, &cfg); err != nil {
			l.Fatal("%s", err)
		}

		// Setup any global configuration options
		done := HandleGlobalFlags(l, cfg)
		defer done()

		// Create the API client
		client := api.NewClient(l, loadAPIClientConfig(cfg, `AgentAccessToken`))

		// Create a new uploader client (S3, GS, Artifactory) to perform the upload.
		var uploaderClient agent.Uploader

		uploaderConfig := agent.ArtifactUploaderConfig{
			JobID:          cfg.Job,
			Paths:          cfg.UploadPaths,
			Destination:    cfg.Destination,
			ContentType:    cfg.ContentType,
			DebugHTTP:      cfg.DebugHTTP,
			FollowSymlinks: cfg.FollowSymlinks,
		}

		// Determine what uploader to use
		if uploaderConfig.Destination != "" {
			if strings.HasPrefix(uploaderConfig.Destination, "s3://") {
				uploaderClient, err = agent.NewS3Uploader(l, agent.S3UploaderConfig{
					Destination: uploaderConfig.Destination,
					DebugHTTP:   uploaderConfig.DebugHTTP,
					DefaultObjectACL: cfg.S3ACL,
				})
			} else if strings.HasPrefix(uploaderConfig.Destination, "gs://") {
				uploaderClient, err = agent.NewGSUploader(l, agent.GSUploaderConfig{
					Destination: uploaderConfig.Destination,
					DebugHTTP:   uploaderConfig.DebugHTTP,
				})
			} else if strings.HasPrefix(uploaderConfig.Destination, "rt://") {
				uploaderClient, err = agent.NewArtifactoryUploader(l, agent.ArtifactoryUploaderConfig{
					Destination: uploaderConfig.Destination,
					DebugHTTP:   uploaderConfig.DebugHTTP,
				})
			} else {
				l.Fatal(fmt.Sprintf("Invalid upload destination: '%v'. Only s3://, gs:// or rt:// upload destinations are allowed. Did you forget to surround your artifact upload pattern in double quotes?", uploaderConfig.Destination))
			}

			l.Info("Uploading to %q, using your agent configuration", uploaderConfig.Destination)
		} else {
			uploaderClient = agent.NewFormUploader(l, agent.FormUploaderConfig{
				DebugHTTP: uploaderConfig.DebugHTTP,
			})

			l.Info("Uploading to default Buildkite artifact storage")
		}

		// Check if creation caused an error
		if err != nil {
			l.Fatal(fmt.Sprintf("Error creating uploader: %v", err))
		}

		// Setup the uploader
		uploader := agent.NewArtifactUploader(l, client, uploaderConfig)

		// Upload the artifacts
		if err := uploader.Upload(uploaderClient); err != nil {
			l.Fatal("Failed to upload artifacts: %s", err)
		}
	},
}
