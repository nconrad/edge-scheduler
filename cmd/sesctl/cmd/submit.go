package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func init() {
	cmdSubmit := &cobra.Command{
		Use:              "submit [FLAGS]",
		Short:            "submit a job to cloud scheduler",
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			submitFunc := func(r *JobRequest) error {
				if r.JobID != "" {
					q, err := url.ParseQuery("id=" + r.JobID + "&dryrun=" + fmt.Sprint(r.DryRun))
					if err != nil {
						return err
					}
					resp, err := r.handler.RequestGet("api/v1/submit", q, r.Headers)
					if err != nil {
						return err
					}
					body, err := r.handler.ParseJSONHTTPResponse(resp)
					if err != nil {
						return err
					}
					blob, _ := json.MarshalIndent(body, "", " ")
					fmt.Printf("%s\n", string(blob))
				} else if r.FilePath != "" {
					q, err := url.ParseQuery("&dryrun=" + fmt.Sprint(r.DryRun))
					if err != nil {
						return err
					}
					resp, err := r.handler.RequestPostFromFileWithQueries("api/v1/submit", r.FilePath, q)
					if err != nil {
						return err
					}
					body, err := r.handler.ParseJSONHTTPResponse(resp)
					if err != nil {
						return err
					}
					blob, _ := json.MarshalIndent(body, "", " ")
					fmt.Printf("%s\n", string(blob))
				} else {
					return fmt.Errorf("Either --job-id or --file-path should be provided.")
				}
				return nil
			}
			return jobRequest.Run(submitFunc)
		},
	}
	flags := cmdSubmit.Flags()
	flags.StringVarP(&jobRequest.JobID, "job-id", "j", "", "Job ID")
	flags.BoolVarP(&jobRequest.DryRun, "dry-run", "", false, "Dry run the job")
	flags.StringVarP(&jobRequest.FilePath, "file-path", "f", "", "Path to the job file")
	rootCmd.AddCommand(cmdSubmit)
}
