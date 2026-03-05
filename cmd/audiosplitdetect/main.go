package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/mxpv/podsync/pkg/audiosig"
)

// Opts defines CLI flags for signature detection.
// Inputs: none (struct definition).
// Outputs: none.
// Example usage:
//
//	opts := Opts{}
//
// Notes: Uses go-flags for parsing.
type Opts struct {
	InputPath        string  `long:"in" description:"input audio file" required:"true"`
	SignaturePath    string  `long:"sig-audio" description:"signature clip file"`
	OutputPath       string  `long:"out" description:"output file path"`
	Trim             bool    `long:"trim" description:"trim input at split timestamp"`
	Copy             bool    `long:"copy" description:"use stream copy for trim"`
	CoarseSampleRate int     `long:"coarse-sr" description:"coarse sample rate (Hz)" default:"4000"`
	RefineSampleRate int     `long:"refine-sr" description:"refine sample rate (Hz)" default:"11025"`
	EnvFPS           int     `long:"env-fps" description:"envelope frames per second" default:"25"`
	MarginSeconds    int     `long:"margin" description:"refine margin seconds" default:"15"`
	TopK             int     `long:"topk" description:"top K coarse peaks" default:"5"`
	MinScore         float64 `long:"min-score" description:"minimum match score" default:"0.6"`
	MinPeakRatio     float64 `long:"min-peak-ratio" description:"minimum peak ratio" default:"1.2"`
}

func main() {
	ctx := context.Background()
	var opts Opts
	if _, err := flags.Parse(&opts); err != nil {
		os.Exit(2)
	}

	cfg := audiosig.Config{
		CoarseSampleRate: opts.CoarseSampleRate,
		RefineSampleRate: opts.RefineSampleRate,
		EnvFPS:           opts.EnvFPS,
		RefineEnvFPS:     opts.EnvFPS,
		Margin:           time.Duration(opts.MarginSeconds) * time.Second,
		FinalMargin:      750 * time.Millisecond,
		ExtraPad:         0,
		TopK:             opts.TopK,
		MinScore:         opts.MinScore,
		MinPeakRatio:     opts.MinPeakRatio,
	}

	if opts.SignaturePath == "" {
		fmt.Fprintln(os.Stderr, "error: -sig-audio is required")
		os.Exit(2)
	}

	sigPath := opts.SignaturePath

	res, err := audiosig.Detect(ctx, opts.InputPath, sigPath, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	printResult(res)

	if opts.Trim {
		if opts.OutputPath == "" {
			fmt.Fprintln(os.Stderr, "error: -out is required when -trim is set")
			os.Exit(2)
		}
		bitrateKbps := 0
		if !opts.Copy {
			inputBitrate, err := audiosig.FFprobeAudioBitrate(ctx, opts.InputPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: bitrate lookup failed: %v\n", err)
			} else {
				bitrateKbps = inputBitrate
			}
		}
		trimCmd := audiosig.TrimCommand(opts.InputPath, opts.OutputPath, res.SplitAt, opts.Copy, bitrateKbps)
		fmt.Printf("TrimCommand: %s\n", trimCmd)
		if res.MatchFound {
			if err := audiosig.RunTrim(ctx, opts.InputPath, opts.OutputPath, res.SplitAt, opts.Copy, bitrateKbps); err != nil {
				fmt.Fprintf(os.Stderr, "trim error: %v\n", err)
				os.Exit(1)
			}
		}
	}
}

// printResult prints a summary of detection results.
// Inputs: result struct.
// Outputs: none (stdout).
// Example usage:
//
//	printResult(res)
//
// Notes: Outputs formatted fields per requirements.
func printResult(res audiosig.Result) {
	fmt.Printf("SignatureFingerprint: %s\n", res.SignatureFingerprint)
	fmt.Printf("InputDuration: %s\n", audiosig.FormatHMS(res.InputDuration))
	if res.MatchFound {
		fmt.Printf("SignatureStart: %s\n", audiosig.FormatHMSMillis(res.SignatureStart))
		fmt.Printf("SignatureEnd: %s\n", audiosig.FormatHMSMillis(res.SignatureEnd))
		newDur := res.InputDuration - res.SplitAt
		if newDur < 0 {
			newDur = 0
		}
		fmt.Printf("NewDuration: %s\n", audiosig.FormatHMSMillis(newDur))
	} else {
		fmt.Printf("SignatureStart: N/A\n")
		fmt.Printf("SignatureEnd: N/A\n")
		fmt.Printf("NewDuration: N/A\n")
	}
	fmt.Printf("MatchFound: %v\n", res.MatchFound)
	fmt.Printf("ConfidenceScore: %.3f%%\n", res.ConfidenceScore*100)
	fmt.Printf("PeakRatio: %.6f\n", res.PeakRatio)
	fmt.Printf("TotalRuntime: %s\n", res.Runtime.Round(time.Millisecond))
}
