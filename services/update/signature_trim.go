package update

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/audiosig"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

type matchedRule struct {
	rule   SignatureRule
	result audiosig.Result
}

// trimEpisodeIfSignatureFound detects and trims using a feed signature if present.
// Inputs:
// - ctx: context for cancellation.
// - feedConfig: feed configuration (uses feed ID for signature directory).
// - episode: episode metadata.
// - source: reader for downloaded episode file.
// Outputs:
// - reader: original or trimmed reader for upload.
// - cleanup: optional cleanup for temp files.
// - err: error if trimming fails.
// Example usage:
//
//	reader, cleanup, err := u.trimEpisodeIfSignatureFound(ctx, feedConfig, episode, tempFile)
//
// Notes: Returns original source if no signature file found.
func (u *Manager) trimEpisodeIfSignatureFound(ctx context.Context, feedConfig *feed.Config, episode *model.Episode, source io.Reader) (io.Reader, func(), error) {
	if episode == nil || feedConfig == nil {
		return source, nil, nil
	}
	if u.sigDir == "" {
		return source, nil, nil
	}
	logger := log.WithFields(log.Fields{"feed_id": feedConfig.ID, "episode_id": episode.ID, "signatures_root": u.sigDir})
	logger.Info("[trim] start")
	sigDir := filepath.Join(u.sigDir, feedConfig.ID, "signatures")
	logger.WithField("signature_dir", sigDir).Info("[trim] signature lookup started")
	rulesPath := filepath.Join(sigDir, "rules.json")
	rules, ok, err := ReadSignatureRules(rulesPath)
	if err != nil {
		return nil, nil, err
	}
	if !ok || len(rules.Rules) == 0 {
		logger.Info("[trim] no rules.json; skipping")
		return source, nil, nil
	}
	logger.WithField("rules_path", rulesPath).Info("[trim] rules loaded")

	logger.Info("[trim] rules loaded; preparing temp input")

	tmpIn, err := os.CreateTemp("", "podsync-episode-*.bin")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp input: %w", err)
	}
	if _, err := io.Copy(tmpIn, source); err != nil {
		_ = tmpIn.Close()
		_ = os.Remove(tmpIn.Name())
		return nil, nil, fmt.Errorf("write temp input: %w", err)
	}
	if err := tmpIn.Close(); err != nil {
		_ = os.Remove(tmpIn.Name())
		return nil, nil, fmt.Errorf("close temp input: %w", err)
	}

	cfg := audiosig.Config{
		CoarseSampleRate: 4000,
		RefineSampleRate: 11025,
		EnvFPS:           25,
		RefineEnvFPS:     25,
		Margin:           15 * time.Second,
		FinalMargin:      750 * time.Millisecond,
		ExtraPad:         0,
		TopK:             5,
		MinScore:         0.6,
		MinPeakRatio:     1.2,
	}
	logger = logger.WithField("input", tmpIn.Name())
	logger.Info("[trim] detection started")
	inputPath := tmpIn.Name()
	var detected []matchedRule
	var inputDur time.Duration
	for idx, rule := range rules.Rules {
		if rule.File == "" || rule.Action == "" {
			logger.WithField("rule_index", idx).Warn("[trim] invalid rule; skipping")
			continue
		}
		sigPath := filepath.Join(sigDir, rule.File)
		if info, err := os.Stat(sigPath); err != nil {
			if os.IsNotExist(err) {
				logger.WithFields(log.Fields{
					"rule_index": idx,
					"rule_file":  rule.File,
					"signature":  sigPath,
				}).Warn("[trim] signature file missing; skipping rule")
				continue
			}
			return nil, nil, fmt.Errorf("stat signature file: %w", err)
		} else if info.Size() == 0 {
			logger.WithFields(log.Fields{
				"rule_index": idx,
				"rule_file":  rule.File,
				"signature":  sigPath,
			}).Warn("[trim] signature file empty; skipping rule")
			continue
		}
		logger.WithFields(log.Fields{
			"rule_index":  idx,
			"rule_file":   rule.File,
			"rule_action": rule.Action,
			"rule_pre":    rule.PreSeconds,
			"rule_post":   rule.PostSeconds,
			"signature":   sigPath,
		}).Info("[trim] applying rule")

		logger = logger.WithField("input", inputPath)
		logger.Info("[trim] detection started")
		result, err := audiosig.Detect(ctx, inputPath, sigPath, cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("signature detect failed: %w", err)
		}
		if !result.MatchFound {
			logger.Info("[trim] signature not detected; skipping rule")
			continue
		}
		if inputDur == 0 {
			inputDur = result.InputDuration
		}
		logger = logger.WithFields(log.Fields{
			"signature_start": result.SignatureStart,
			"signature_end":   result.SignatureEnd,
			"split_at":        result.SplitAt,
			"confidence":      result.ConfidenceScore,
		})
		logger.Info("[trim] signature detected")
		detected = append(detected, matchedRule{rule: rule, result: result})
	}
	if len(detected) == 0 {
		logger.Info("[trim] no matching signatures; skipping")
		file, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, fmt.Errorf("open input: %w", err)
		}
		cleanup := func() {
			_ = file.Close()
			_ = os.Remove(tmpIn.Name())
		}
		return file, cleanup, nil
	}

	logger.Info("[trim] applying matched rules in a single pass")
	newInput, newCleanup, err := u.applyMatchedRules(ctx, inputPath, inputDur, detected, logger)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.Open(newInput)
	if err != nil {
		newCleanup()
		return nil, nil, fmt.Errorf("open final input: %w", err)
	}
	cleanup := func() {
		_ = file.Close()
		newCleanup()
		_ = os.Remove(tmpIn.Name())
	}
	return file, cleanup, nil
}

// findSignatureFile finds the first audio signature file under /data/signatures/<feedID>.
// Inputs: feedID.
// Outputs: signature file path or empty string if none.
// Example usage:
//
//	sigPath, err := u.findSignatureFile("crowder")
//
// Notes: Returns first matching file by extension.
func (u *Manager) findSignatureFile(feedID string) (string, error) {
	if feedID == "" {
		return "", nil
	}
	baseDir := filepath.Join(u.sigDir, feedID, "signatures")
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read signatures dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".mp3", ".wav", ".m4a", ".flac", ".ogg", ".aac":
			return filepath.Join(baseDir, name), nil
		}
	}
	return "", nil
}
