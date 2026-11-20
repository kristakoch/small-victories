package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

/*

Improvements tbd:
- if the file is no longer in the folder but it is in the state, delete it from the state
- my process is to name the file after the date, then add the title to it,
  meaning as the code is now those file states will build up

*/

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
		// Configured omissions.
		if ofn == fn {
			return true
		}
		// This is a typora thing.
		if strings.HasSuffix(fn, "~.md") {
			return true
		}
	}
	return false
}

func main() {
	var err error

	// Edit these before installing the app.
	var cfg = config{
		watchFolder: "/Users/krista/Library/Mobile Documents/com~apple~CloudDocs/Documents/notes/misc/writing/stories",
		goalWords:   3333,
		omitFiles:   []string{"think.md"},
	}

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
	LastCelebratedOn string               `json:"lastCelebratedOn"` // last celebrated
	CelebratedOn     []string             `json:"celebratedOn"`     // basic history of goals reached
	Files            map[string]fileState `json:"files"`            // state of all files in the folder
	Record           int                  `json:"record"`           // all time record
	RecordSetOn      string               `json:"recordSetOn"`      // record date
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

	if err = updateState(s, cfg); nil != err {
		return err
	}

	var wordsToday int
	for fn, fs := range s.Files {
		// Ignore any omitted files if they made it into the state.
		if cfg.omit(fn) {
			continue
		}

		// Total up new words for today.
		// These are net amounts. Disallow lowering the total.
		if fwt := fs.Words - fs.WordsDayStart; fwt > 0 {
			wordsToday += fwt
		}
	}

	log.Printf("user has written %d words today\n", wordsToday)

	alreadyCelebratedGoal := isToday(s.LastCelebratedOn)

	// Celebrate a goal.
	if (wordsToday >= cfg.goalWords) && !alreadyCelebratedGoal {
		if err = celebrateGoal(cfg.goalWords); nil != err {
			return err
		}
		s.LastCelebratedOn = time.Now().Format(dayDateFormat)
		s.CelebratedOn = append(s.CelebratedOn, s.LastCelebratedOn)
	}

	alreadyCelebratedRecord := isToday(s.RecordSetOn)

	// Celebrate a record.
	if wordsToday >= int(s.Record) {
		if !alreadyCelebratedRecord {
			if err = celebrateRecord(wordsToday); nil != err {
				return err
			}
			s.RecordSetOn = time.Now().Format(dayDateFormat)
		}

		s.Record = wordsToday
	}

	// Since the app uses net amounts, if the record was set today but
	// the current number of words is lower than the record, bump it down.
	if (wordsToday < s.Record) && alreadyCelebratedRecord {
		s.Record = wordsToday
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

func celebrateGoal(
	goal int,
) error {
	var err error

	script := fmt.Sprintf(`display notification "Great job writing %d words today 🤘"  with title "NEW ACHIEVEMENT" sound name "Glass"`, goal)

	cmd := exec.Command("osascript", "-e", script)

	if err = cmd.Run(); nil != err {
		return err
	}

	return nil
}

func celebrateRecord(
	newRecord int,
) error {
	var err error

	script := fmt.Sprintf(`display notification "You set a new record of %d words today 🍾"  with title "NEW ACHIEVEMENT" sound name "Glass"`, newRecord)

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
