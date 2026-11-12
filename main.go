package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

var (
	stateFile     = "state.json"
	dayDateFormat = "2006-01-02"
)

type config struct {
	goalWords   int
	watchFolder string
	omitFiles   []string
}

func (cfg config) validate() error {
	if "" == cfg.watchFolder {
		log.Fatal("specify a watch folder")
	}
	if cfg.goalWords < 1 {
		log.Fatal("specify a goal word amount")
	}
	return nil
}

func (cfg config) omit(fn string) bool {
	for _, ofn := range cfg.omitFiles {
		if ofn == fn {
			return true
		}
	}
	return false
}

func main() {
	var err error

	var cfg config
	cfg.watchFolder = os.Getenv("WATCH_FOLDER")
	cfg.goalWords, _ = strconv.Atoi(os.Getenv("GOAL_WORDS"))
	cfg.omitFiles = strings.Split(os.Getenv("OMIT_FILES"), ",")

	if err = cfg.validate(); nil != err {
		log.Fatal(err)
	}

	// Create new watcher.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	// Start listening for events.
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
					log.Println("modified file:", event.Name)

					if err = run(cfg); nil != err {
						log.Println("error running word count script:", err)
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}
		}
	}()

	// Add a path.
	err = watcher.Add(cfg.watchFolder)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("watching for updates to files in folder %s", cfg.watchFolder)

	// Block main goroutine forever.
	<-make(chan struct{})
}

func run(
	cfg config,
) error {
	var err error

	// If we don't have a file keeping track of state yet, create one.
	var s *state
	if s, err = getState(cfg); nil != err {
		return err
	}

	// If the goal amount of new words was added, celebrate.
	if err = checkFiles(s, cfg); nil != err {
		return err
	}

	return nil
}

type state struct {
	LastCelebratedOn string               `json:"lastCelebratedOn"` //
	CelebratedOn     []string             `json:"celebratedOn"`     // basic history of goals reached
	Files            map[string]fileState `json:"files"`            // state of all files in the folder
}

type fileState struct {
	Words         int    `json:"words"`         // ex: 150
	WordsDayStart int    `json:"wordsDayStart"` // ex: 100
	LastSavedOn   string `json:"lastSavedOn"`   // ex: 2026-11-12
}

func checkFiles(
	s *state,
	cfg config,
) error {
	var err error

	if isToday(s.LastCelebratedOn) {
		log.Printf("user has already celebrated reaching goal of %d words for %s, exiting", cfg.goalWords, s.LastCelebratedOn)
		return nil
	}

	if err = updateState(s, cfg); nil != err {
		return err
	}

	var wordsToday int
	for _, fs := range s.Files {

		// Total up new words for today.
		// These are net amounts. Disallow lowering the total.
		if fwt := fs.Words - fs.WordsDayStart; fwt > 0 {
			wordsToday += fwt
		}
	}

	log.Printf("user has written %d words today\n", wordsToday)

	if wordsToday >= cfg.goalWords {
		if err = celebrate(cfg); nil != err {
			return err
		}
		s.LastCelebratedOn = time.Now().Format(dayDateFormat)
		s.CelebratedOn = append(s.CelebratedOn, s.LastCelebratedOn)
	}

	if err = storeState(s); nil != err {
		return err
	}

	return nil
}

func getState(
	cfg config,
) (*state, error) {
	var err error
	var s state

	if _, err = os.Stat(stateFile); nil != err {
		if !os.IsNotExist(err) {
			return &s, err
		}
	}

	if nil == err {
		var bs []byte
		if bs, err = os.ReadFile(stateFile); nil != err {
			return &s, err
		}
		if err = json.Unmarshal(bs, &s); nil != err {
			return &s, err
		}
		return &s, nil
	}

	s = state{
		Files:            make(map[string]fileState),
		LastCelebratedOn: "",
		CelebratedOn:     make([]string, 0),
	}

	if err = updateState(&s, cfg); nil != err {
		return nil, err
	}

	if err = storeState(&s); nil != err {
		return nil, err
	}

	log.Printf("state file created at %s\n", stateFile)

	return &s, nil
}

func updateState(
	s *state,
	cfg config,
) error {
	var err error

	entries, err := os.ReadDir(cfg.watchFolder)
	if err != nil {
		log.Fatal(err)
	}

	for _, entry := range entries {
		if entry.IsDir() || cfg.omit(entry.Name()) {
			continue
		}

		filePath := filepath.Join(cfg.watchFolder, entry.Name())

		file, err := os.Open(filePath)
		if err != nil {
			log.Printf("failed to open %s: %v\n", filePath, err)
			continue
		}

		var wc int
		if wc, err = countWords(filePath); nil != err {
			return err
		}

		n := time.Now()

		var fs fileState
		var ok bool
		if fs, ok = s.Files[entry.Name()]; !ok {
			fs = fileState{
				Words:         wc,
				WordsDayStart: wc,
				LastSavedOn:   n.Format(dayDateFormat),
			}
		}

		// Update the number of words.
		fs.Words = wc

		// If this is a new day, update words at day start to the current amount
		if !isToday(fs.LastSavedOn) {
			fs.WordsDayStart = wc
		}

		// Update stored date.
		fs.LastSavedOn = n.Format(dayDateFormat)

		// Save back to the file registry.
		s.Files[entry.Name()] = fs

		// Close immediately inside the loop to avoid "too many open files" errors
		file.Close()
	}

	return nil
}

func countWords(fn string) (int, error) {
	var err error

	var bs []byte
	if bs, err = os.ReadFile(fn); nil != err {
		return 0, err
	}

	words := strings.Fields(string(bs))

	return len(words), nil
}

func celebrate(
	cfg config,
) error {
	var err error

	script := fmt.Sprintf(`display notification "Great job writing %d words 🤘"  with title "NEW ACHIEVEMENT" sound name "Glass"`, cfg.goalWords)

	cmd := exec.Command("osascript", "-e", script)

	if err = cmd.Run(); nil != err {
		return err
	}

	return nil
}

func isToday(lastCelebratedOn string) bool {
	if "" == lastCelebratedOn {
		return false
	}
	return lastCelebratedOn == time.Now().Format(dayDateFormat)
}

func storeState(s *state) error {
	var err error

	var bs []byte
	if bs, err = json.MarshalIndent(s, "", "  "); nil != err {
		return err
	}

	if err = os.WriteFile(stateFile, bs, 0644); nil != err {
		return err
	}

	return nil
}
