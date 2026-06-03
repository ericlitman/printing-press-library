package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePodcastScript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "episode.md")
	script := `---
title: "The Focus Premium"
episode: 7
language: en
model: eleven_v3
output_format: mp3_44100_192
loudness: -16
cast:
  HOST: Rachel
  GUEST: Antoni
music:
  intro: { prompt: "warm intro", seconds: 12 }
  outro: { prompt: "soft outro", seconds: 10 }
  bed: { prompt: "ambient bed", duck_db: -15 }
---

[intro]

HOST: Welcome back.
GUEST: Glad to be here.

[music: bed]
HOST: Let's start.
[sfx: page turn, 1.5s]
GUEST: Perfect.
[music: stop]

[outro]
`
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	episode, err := parsePodcastScript(path)
	if err != nil {
		t.Fatal(err)
	}
	if episode.Title != "The Focus Premium" {
		t.Fatalf("title = %q", episode.Title)
	}
	if episode.TextChars == 0 {
		t.Fatal("expected text chars")
	}
	if got := len(episode.Segments); got != 6 {
		t.Fatalf("segments = %d", got)
	}
	if episode.Segments[2].BedName != "bed" {
		t.Fatalf("bed segment = %q", episode.Segments[2].BedName)
	}
	if episode.Segments[3].Kind != "sfx" || episode.Segments[3].SFXSeconds != 1.5 {
		t.Fatalf("unexpected sfx segment: %+v", episode.Segments[3])
	}
}

func TestParsePodcastScriptUnknownSpeaker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.md")
	script := `---
cast:
  HOST: Rachel
---
GUEST: Hello.
`
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := parsePodcastScript(path)
	if err == nil || !strings.Contains(err.Error(), "not in cast") {
		t.Fatalf("expected cast error, got %v", err)
	}
}

func TestApplyPodcastOverridesHonorsExplicitFlags(t *testing.T) {
	episode := podcastEpisode{Model: "front-model", OutputFormat: "pcm_44100", Loudness: -14, Cast: map[string]string{}, Music: map[string]podcastMusic{}}
	applyPodcastOverrides(&episode, podcastProduceOptions{Model: "eleven_v3", OutputFormat: "mp3_44100_192", Loudness: -16})
	if episode.Model != "front-model" || episode.OutputFormat != "pcm_44100" || episode.Loudness != -14 {
		t.Fatalf("front matter was overwritten: %+v", episode)
	}
	applyPodcastOverrides(&episode, podcastProduceOptions{Model: "eleven_v3", ModelSet: true, OutputFormat: "mp3_44100_192", OutputFormatSet: true, Loudness: -16, LoudnessSet: true})
	if episode.Model != "eleven_v3" || episode.OutputFormat != "mp3_44100_192" || episode.Loudness != -16 {
		t.Fatalf("explicit overrides not applied: %+v", episode)
	}
}

func TestHashPodcastSegmentIncludesVoiceIDs(t *testing.T) {
	segment := podcastSegment{Text: "hello", Turns: []podcastTurn{{Speaker: "HOST", Text: "hello"}}}
	a := hashPodcastSegment(segment, map[string]elevenVoice{"HOST": {VoiceID: "voice-a"}}, "model", "mp3")
	b := hashPodcastSegment(segment, map[string]elevenVoice{"HOST": {VoiceID: "voice-b"}}, "model", "mp3")
	if a == b {
		t.Fatal("expected voice assignment to affect resume hash")
	}
}

func TestParseLoudnormMeasurement(t *testing.T) {
	output := `frame=1
{
	"input_i" : "-21.72",
	"input_tp" : "-3.14",
	"input_lra" : "8.20",
	"input_thresh" : "-31.80",
	"output_i" : "-16.01",
	"target_offset" : "0.02"
}`
	m, err := parseLoudnormMeasurement(output)
	if err != nil {
		t.Fatal(err)
	}
	if m.TargetOffset != "0.02" {
		t.Fatalf("offset = %q", m.TargetOffset)
	}
}

func TestBuildPodcastFilterGraphNormalizesAndDucks(t *testing.T) {
	graph := buildPodcastFilterGraph([]podcastMixItem{
		{Kind: "intro", Path: "intro.mp3", DurationSeconds: 3},
		{Kind: "voice", Path: "voice.mp3", BedPath: "bed.mp3", DurationSeconds: 4},
		{Kind: "outro", Path: "outro.mp3", DurationSeconds: 3},
	})
	for _, want := range []string{
		"aresample=44100,aformat=sample_fmts=fltp:channel_layouts=stereo",
		"aloop=loop=-1:size=176400",
		"sidechaincompress=threshold=0.03",
		"acrossfade=d=1.0",
	} {
		if !strings.Contains(graph, want) {
			t.Fatalf("filter graph missing %q:\n%s", want, graph)
		}
	}
}

func TestPodcastMasterVariants(t *testing.T) {
	variants := podcastMasterVariants(podcastMasterOptions{
		Out:        "episode.mp3",
		TargetLUFS: -16,
		TruePeak:   -1,
		Variants:   "apple,spotify",
	})
	if len(variants) != 2 {
		t.Fatalf("variants = %d", len(variants))
	}
	if variants[0].Path != "episode-apple.mp3" || variants[0].TargetLUFS != -16 {
		t.Fatalf("apple variant = %+v", variants[0])
	}
	if variants[1].Path != "episode-spotify.mp3" || variants[1].TargetLUFS != -14 {
		t.Fatalf("spotify variant = %+v", variants[1])
	}
}

func TestLoudnormPass(t *testing.T) {
	if !loudnormPass(loudnormMeasurement{InputI: "-16.1", InputTP: "-1.2"}, -16, -1) {
		t.Fatal("expected loudnorm pass")
	}
	if loudnormPass(loudnormMeasurement{InputI: "-18.5", InputTP: "-0.1"}, -16, -1) {
		t.Fatal("expected loudnorm failure")
	}
}

func TestBuildPodcastSEOAssets(t *testing.T) {
	transcript := "HOST: Attention is the new luxury. GUEST: The product is protecting focus. HOST: That changes how teams work."
	assets := buildPodcastSEOAssets(transcript, "deep work", []string{"focus", "productivity"})
	if len(assets.Titles) != 3 {
		t.Fatalf("titles = %d", len(assets.Titles))
	}
	if !strings.Contains(assets.Notes, "focus, productivity") {
		t.Fatalf("notes missing keywords:\n%s", assets.Notes)
	}
	if len(assets.Quotes) == 0 {
		t.Fatal("expected pull quotes")
	}
}

func TestBlocksToSRT(t *testing.T) {
	srt := blocksToSRT([]string{"HOST: First.", "GUEST: Second."})
	for _, want := range []string{"1\n00:00:00,000 --> 00:00:30,000", "2\n00:00:30,000 --> 00:01:00,000"} {
		if !strings.Contains(srt, want) {
			t.Fatalf("srt missing %q:\n%s", want, srt)
		}
	}
	vtt := blocksToVTT([]string{"HOST: Well, yes."})
	if !strings.Contains(vtt, "00:00:00.000 --> 00:00:30.000") {
		t.Fatalf("vtt timestamp not converted:\n%s", vtt)
	}
	if !strings.Contains(vtt, "Well, yes.") {
		t.Fatalf("vtt corrupted transcript comma:\n%s", vtt)
	}
}

func TestScorePodcastClipCandidates(t *testing.T) {
	transcript := "Why does focus disappear so quickly? The practical lesson is that teams need a framework because attention is fragile. This is short."
	candidates := scorePodcastClipCandidates(clipWindowsFromText(transcript), 45)
	if len(candidates) < 2 {
		t.Fatalf("candidates = %d", len(candidates))
	}
	if candidates[0].Score < candidates[1].Score {
		t.Fatalf("candidates not sorted: %+v", candidates)
	}
	selected := selectPodcastClips(candidates, 1, 60)
	if len(selected) != 1 {
		t.Fatalf("selected = %d", len(selected))
	}
}

func TestClipWindowsFromSTTUsesWordTimings(t *testing.T) {
	windows := clipWindowsFromSTT([]byte(`{"words":[
		{"text":"Why","start":12.5,"end":12.8},
		{"text":"does","start":12.9,"end":13.1},
		{"text":"focus","start":13.2,"end":13.6},
		{"text":"matter?","start":13.7,"end":14.1},
		{"text":"Teams","start":21.0,"end":21.4},
		{"text":"need","start":21.5,"end":21.8},
		{"text":"a","start":21.9,"end":22.0},
		{"text":"framework.","start":22.1,"end":22.7}
	]}`))
	if len(windows) != 2 {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[0].Start != 12.5 || windows[0].End != 14.1 {
		t.Fatalf("first window timing = %+v", windows[0])
	}
}

func TestAspectSuffix(t *testing.T) {
	if got := aspectSuffix("9:16"); got != "9x16" {
		t.Fatalf("aspect suffix = %q", got)
	}
}

func TestPodcastVoicePreviewSelection(t *testing.T) {
	previews := parsePodcastVoicePreviews([]byte(`{"previews":[{"generated_voice_id":"gen1","preview_url":"https://example.test/1.mp3"},{"generated_voice_id":"gen2"}]}`))
	if len(previews) != 2 {
		t.Fatalf("previews = %d", len(previews))
	}
	if got := choosePodcastGeneratedVoice(previews, "2"); got != "gen2" {
		t.Fatalf("pick by index = %q", got)
	}
	if got := choosePodcastGeneratedVoice(previews, "gen1"); got != "gen1" {
		t.Fatalf("pick by id = %q", got)
	}
}

func TestUpsertPodcastShowBibleVoice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "show.yaml")
	if err := upsertPodcastShowBibleVoice(path, "bestself", "HOST", "voice123", "designed", "42", "warm host"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`slug: bestself`, `HOST:`, `voice: "voice123"`, `voice_kind: designed`, `voice_seed: "42"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("show bible missing %q:\n%s", want, text)
		}
	}
	if err := upsertPodcastShowBibleVoice(path, "bestself", "HOST", "voice456", "designed", "99", "clear host"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text = string(data)
	if strings.Count(text, "  HOST:\n") != 1 {
		t.Fatalf("expected one HOST block:\n%s", text)
	}
	if strings.Contains(text, `voice: "voice123"`) || !strings.Contains(text, `voice: "voice456"`) {
		t.Fatalf("speaker block was not updated:\n%s", text)
	}
}

func TestRenderPodcastAutoScriptValidates(t *testing.T) {
	bible := defaultPodcastShowBible("bestself-focus")
	script := renderPodcastAutoScript(bible, podcastAutoOptions{Show: "bestself-focus", Topic: "deep work", Duration: 12, Model: "eleven_v3"}, "deep work", "attention needs protection")
	episode, err := parsePodcastScriptText(script)
	if err != nil {
		t.Fatal(err)
	}
	if len(episode.Segments) == 0 {
		t.Fatal("expected generated segments")
	}
	if episode.Cast["HOST"] == "" || episode.Music["intro"].Prompt == "" {
		t.Fatalf("missing cast/music in generated script:\n%s", script)
	}
}

func TestFormatAndReadPodcastShowBible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "show.yaml")
	bible := defaultPodcastShowBible("bestself-focus")
	bible.Title = "BestSelf Focus"
	if err := os.WriteFile(path, []byte(formatPodcastShowBible(bible)), 0o644); err != nil {
		t.Fatal(err)
	}
	read, err := readPodcastShowBible(path, "bestself-focus")
	if err != nil {
		t.Fatal(err)
	}
	if read.Title != "BestSelf Focus" || read.Cast["HOST"] == "" {
		t.Fatalf("read bible = %+v", read)
	}
}
