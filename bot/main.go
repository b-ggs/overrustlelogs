package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/b-ggs/overrustlelogs/common"
)

// log paths
const (
	destinyPath = "Destinygg chatlog"
	twitchPath  = "Destiny chatlog"
	LogsPath = "/logs"
	IgnoreListPath = "/bot/ignore.json"
	IgnoreLogListPath = "/bot/ignorelog.json"
)

var validNick = regexp.MustCompile("^[a-zA-Z0-9_]+$")
var configPath string

func init() {
	flag.StringVar(&configPath, "config", "/bot/overrustlelogs.toml", "config path")
	flag.Parse()
	common.SetupConfig(configPath)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	c := common.NewDestiny()
	b := NewBot(c)
	go b.Run()
	go c.Run()

	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
	<-sigint
	b.Stop()
	log.Println("i love you guys, be careful")
	os.Exit(0)
}

type command func(m *common.Message, r *bufio.Reader) (string, error)

// Bot commands
type Bot struct {
	c           *common.Destiny
	start       time.Time
	nukeEOL     time.Time
	nukeText    []byte
	autoMutes   []string
	lastLine    string
	cooldownEOL time.Time
	public      map[string]command
	private     map[string]command
	admins      map[string]struct{}
	ignore      map[string]struct{}
	ignoreLog   map[string]struct{}
}

// NewBot ...
func NewBot(c *common.Destiny) *Bot {
	b := &Bot{
		c:         c,
		start:     time.Now(),
		autoMutes: make([]string, 0),
		admins:    make(map[string]struct{}, len(common.GetConfig().Bot.Admins)),
		ignoreLog: make(map[string]struct{}),
	}
	for _, admin := range common.GetConfig().Bot.Admins {
		b.admins[admin] = struct{}{}
	}
	b.public = map[string]command{
		"add":      b.handleMute,
		"del":      b.handleMuteRemove,
		"log":      b.handleDestinyLogs,
		"tlog":     b.handleTwitchLogs,
		"logs":     b.handleDestinyLogs,
		"tlogs":    b.handleTwitchLogs,
		"mentions": b.handleMentions,
		"nuke":     b.handleSimpleNuke,
		"aegis":    b.handleAegis,
		"bans":     b.handleBans,
		"subs":     b.handleSubs,
		"top100":   b.handleTop100,
	}
	b.private = map[string]command{
		"log":         b.handleDestinyLogs,
		"tlog":        b.handleTwitchLogs,
		"logs":        b.handleDestinyLogs,
		"tlogs":       b.handleTwitchLogs,
		"uptime":      b.handleUptime,
		"ignore":      b.handleIgnore,
		"unignore":    b.handleUnignore,
		"ignorelog":   b.handleIgnoreLog,
		"unignorelog": b.handleUnignoreLog,
	}

	b.ignore = make(map[string]struct{})
	if d, err := ioutil.ReadFile(IgnoreListPath); err == nil {
		ignore := []string{}
		if err := json.Unmarshal(d, &ignore); err == nil {
			for _, nick := range ignore {
				b.addIgnore(nick)
			}
		}
	}
	if d, err := ioutil.ReadFile(IgnoreLogListPath); err == nil {
		ignoreLog := []string{}
		if err := json.Unmarshal(d, &ignoreLog); err == nil {
			for _, nick := range ignoreLog {
				b.addIgnoreLog(nick)
			}
		}
	}

	return b
}

// Run starts bot
func (b *Bot) Run() {
	var messageCount int
	for m := range b.c.Messages() {
		admin := b.isAdmin(m.Nick)
		switch m.Type {
		case "MSG":
			messageCount++
			if (!time.Now().After(b.cooldownEOL) && !admin) || b.isIgnored(m.Nick) {
				continue
			}
			rs, err := b.runCommand(b.public, m)
			if err != nil {
				continue
			}
			if rs == "" {
				err := b.c.Whisper(m.Nick, "user not found")
				if err != nil {
					log.Println("error sending whisper: ", err)
				}
				continue
			}
			if b.isNuked(rs) || b.isInAutoMute(rs) {
				err := b.c.Message(fmt.Sprintf("Ignoring %s from now on. SOTRIGGERED", m.Nick))
				if err != nil {
					log.Println("error sending message: ", err)
				}
				b.addIgnore(m.Nick)
				continue
			}
			if rs == b.lastLine && !admin {
				if messageCount < 16 {
					continue
				} else {
					rs += " ."
					messageCount = 0
				}
			}
			if admin {
				rs += " SWEATSTINY"
			}
			if rs == b.lastLine && admin {
				rs += " ."
			}
			err = b.c.Message(rs)
			if err != nil {
				log.Println(err)
				continue
			}
			// log.Println(m.Nick, m.Data, "> send:", rs)
			b.cooldownEOL = time.Now().Add(10 * time.Second)
			b.lastLine = rs
		case "PRIVMSG":
			rs, err := b.runCommand(b.private, m)
			if err != nil || rs == "" {
				log.Println(err)
				continue
			}
			if err = b.c.Whisper(m.Nick, rs); err != nil {
				log.Println(err)
			}
		}
	}
}

// Stop bot
func (b *Bot) Stop() {
	b.c.Stop()
	ignore := []string{}
	for nick := range b.ignore {
		ignore = append(ignore, nick)
	}
	data, _ := json.Marshal(ignore)
	if err := ioutil.WriteFile(IgnoreListPath, data, 0644); err != nil {
		log.Printf("unable to write ignore list %s", err)
		return
	}
	ignoreLog := []string{}
	for nick := range b.ignoreLog {
		ignoreLog = append(ignoreLog, nick)
	}
	data, _ = json.Marshal(ignoreLog)
	if err := ioutil.WriteFile(IgnoreLogListPath, data, 0644); err != nil {
		log.Printf("unable to write ignorelog list %s", err)
	}
}

func (b *Bot) runCommand(commands map[string]command, m *common.Message) (string, error) {
	if m.Data[0] != '!' {
		return "", errors.New("not a command")
	}
	r := bufio.NewReader(bytes.NewReader([]byte(m.Data[1:])))
	c, err := r.ReadString(' ')
	if err != nil && err != io.EOF {
		return "", err
	}
	if err != io.EOF {
		c = c[:len(c)-1]
	}
	c = strings.ToLower(c)
	cmd, ok := commands[c]
	if !ok {
		return "", errors.New("not a valid command")
	}
	return cmd(m, r)
}

func (b *Bot) isNuked(text string) bool {
	r, err := regexp.Compile(string(b.nukeText))
	if err != nil {
		return false
	}
	return b.nukeEOL.After(time.Now()) && r.Match([]byte(text))
}

func (b *Bot) isAdmin(nick string) bool {
	_, ok := b.admins[nick]
	return ok
}

func (b *Bot) isIgnored(nick string) bool {
	_, ok := b.ignore[strings.ToLower(nick)]
	return ok
}

func (b *Bot) isLogIgnored(nick string) bool {
	_, ok := b.ignoreLog[strings.ToLower(nick)]
	return ok
}

func (b *Bot) addIgnore(nick string) {
	b.ignore[strings.ToLower(nick)] = struct{}{}
}

func (b *Bot) removeIgnore(nick string) {
	delete(b.ignore, strings.ToLower(nick))
}

func (b *Bot) addIgnoreLog(nick string) {
	b.ignoreLog[strings.ToLower(nick)] = struct{}{}
}

func (b *Bot) removeIgnoreLog(nick string) {
	delete(b.ignoreLog, strings.ToLower(nick))
}

func (b *Bot) toURL(host string, path string) string {
	var u, err = url.Parse(host)
	if err != nil {
		log.Fatalf("error parsing configured log host %s", err)
	}
	u.Scheme = ""
	u.Path = path
	return u.String()[2:]
}

func (b *Bot) handleIgnoreLog(m *common.Message, r *bufio.Reader) (string, error) {
	if !b.isAdmin(m.Nick) {
		return "", fmt.Errorf("%s is not a admin", m.Nick)
	}
	nick, err := ioutil.ReadAll(r)
	if err != nil || !validNick.Match(nick) {
		return "Invalid nick", err
	}
	b.addIgnoreLog(string(nick))
	return "", nil
}

func (b *Bot) handleUnignoreLog(m *common.Message, r *bufio.Reader) (string, error) {
	if !b.isAdmin(m.Nick) {
		return "", fmt.Errorf("%s is not a admin", m.Nick)
	}
	nick, err := ioutil.ReadAll(r)
	if err != nil || !validNick.Match(nick) {
		return "Invalid nick", err
	}
	if b.isLogIgnored(string(nick)) {
		b.removeIgnoreLog(string(nick))
	}
	return "", nil
}

func (b *Bot) handleIgnore(m *common.Message, r *bufio.Reader) (string, error) {
	if !b.isAdmin(m.Nick) {
		return "", fmt.Errorf("%s is not a admin", m.Nick)
	}
	nick, err := ioutil.ReadAll(r)
	if err != nil || !validNick.Match(nick) {
		return "Invalid nick", err
	}
	b.addIgnore(string(nick))
	return "", nil
}

func (b *Bot) handleUnignore(m *common.Message, r *bufio.Reader) (string, error) {
	if !b.isAdmin(m.Nick) {
		return "", fmt.Errorf("%s is not a admin", m.Nick)
	}
	nick, err := ioutil.ReadAll(r)
	if err != nil || !validNick.Match(nick) {
		return "Invalid nick", err
	}
	if b.isIgnored(string(nick)) {
		b.removeIgnore(string(nick))
	}
	return "", nil
}

func (b *Bot) handleDestinyLogs(m *common.Message, r *bufio.Reader) (string, error) {
	rs, s, err := b.searchNickFromLine(destinyPath, r)
	if err != nil {
		return s, err
	}

	if rs != nil {
		return rs.Month() + " logs. " + b.toURL(common.GetConfig().DestinyGG.LogHost, "/"+rs.Nick()), nil
	}
	return b.toURL(common.GetConfig().DestinyGG.LogHost, ""), nil
}

func (b *Bot) handleTwitchLogs(m *common.Message, r *bufio.Reader) (string, error) {
	rs, s, err := b.searchNickFromLine(twitchPath, r)
	if err != nil {
		return s, err
	}

	if rs != nil {
		return rs.Month() + " logs. " + b.toURL(common.GetConfig().Twitch.LogHost, "/Destiny/"+rs.Nick()), nil
	}
	return b.toURL(common.GetConfig().Twitch.LogHost, "/Destiny"), nil
}

func (b *Bot) searchNickFromLine(path string, r *bufio.Reader) (*common.NickSearchResult, string, error) {
	nick, err := r.ReadString(' ')
	nick = strings.TrimSpace(nick)
	if (err != nil && err != io.EOF) || len(nick) < 1 || b.isLogIgnored(nick) {
		return nil, "", nil
	}
	if !validNick.Match([]byte(nick)) {
		return nil, "", errors.New("invalid nick")
	}
	s, err := common.NewNickSearch(LogsPath+"/"+path, nick)
	if err != nil {
		return nil, "", err
	}
	rs, err := s.Next()
	if err != nil {
		return nil, "No logs found for that user.", err
	}

	return rs, "", nil
}

func (b *Bot) handleSimpleNuke(m *common.Message, r *bufio.Reader) (string, error) {
	return b.handleNuke(m, 10*time.Minute, r)
}

func (b *Bot) handleMute(m *common.Message, r *bufio.Reader) (string, error) {
	if !b.isAdmin(m.Nick) {
		return "", fmt.Errorf("%s is not a admin", m.Nick)
	}
	text, err := ioutil.ReadAll(r)
	if err != nil {
		return "", err
	}
	b.autoMutes = append(b.autoMutes, string(text))
	return "", nil
}

func (b *Bot) handleMuteRemove(m *common.Message, r *bufio.Reader) (string, error) {
	if !b.isAdmin(m.Nick) {
		return "", fmt.Errorf("%s is not a admin", m.Nick)
	}
	text, err := ioutil.ReadAll(r)
	if err != nil {
		return "", err
	}
	for i, v := range b.autoMutes {
		if v == string(text) {
			b.autoMutes = append(b.autoMutes[:i], b.autoMutes[i+1:]...)
			return "", nil
		}
	}
	return "", nil
}

func (b *Bot) isInAutoMute(text string) bool {
	for _, v := range b.autoMutes {
		if strings.Contains(text, v) {
			return true
		}
	}
	return false
}

func (b *Bot) handleNuke(m *common.Message, d time.Duration, r io.Reader) (string, error) {
	if !b.isAdmin(m.Nick) {
		return "", fmt.Errorf("%s is not a admin", m.Nick)
	}
	text, err := ioutil.ReadAll(r)
	if err != nil {
		return "", err
	}
	b.nukeEOL = time.Now().Add(d)
	b.nukeText = bytes.ToLower(text)
	return "", nil
}

func (b *Bot) handleMentions(m *common.Message, r *bufio.Reader) (string, error) {
	if r.Buffered() < 1 {
		return fmt.Sprintf("%s dgg.overrustlelogs.net/mentions/%s", m.Nick, m.Nick), nil
	}
	d, err := ioutil.ReadAll(r)
	if err != nil {
		return "", err
	}
	date, err := time.Parse("2006-01-02", string(d))
	if err != nil {
		return fmt.Sprintf("%s dgg.overrustlelogs.net/mentions/%s", m.Nick, m.Nick), nil
	}
	if date.UTC().After(time.Now().UTC()) {
		return fmt.Sprintf("%s BASEDWATM8 i can't look into the future.", m.Nick), nil
	}
	return fmt.Sprintf("%s dgg.overrustlelogs.net/mentions/%s?date=%s", m.Nick, m.Nick, date.Format("2006-01-02")), nil
}

func (b *Bot) handleAegis(m *common.Message, r *bufio.Reader) (string, error) {
	if !b.isAdmin(m.Nick) {
		return "", fmt.Errorf("%s is not a admin", m.Nick)
	}
	b.nukeEOL = time.Now()
	return "", nil
}

func (b *Bot) handleBans(m *common.Message, r *bufio.Reader) (string, error) {
	return b.toURL(common.GetConfig().DestinyGG.LogHost, "/Ban"), nil
}

func (b *Bot) handleSubs(m *common.Message, r *bufio.Reader) (string, error) {
	return b.toURL(common.GetConfig().DestinyGG.LogHost, "/Subscriber"), nil
}

func (b *Bot) handleUptime(m *common.Message, r *bufio.Reader) (string, error) {
	return time.Since(b.start).String(), nil
}

func (b *Bot) handleTop100(m *common.Message, r *bufio.Reader) (string, error) {
	lastmonth := time.Now().UTC().AddDate(0, -1, 0).Format("January 2006")
	return "https://overrustlelogs.net/Destinygg%20chatlog/" + strings.Replace(lastmonth, " ", "%20", -1) + "/top100", nil
}
