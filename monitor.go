package sapphire

import (
  "github.com/bwmarrin/discordgo"
  "strings"
  "regexp"
  "fmt"
)

type MonitorHandler func(bot *Bot, ctx *MonitorContext)

type Monitor struct {
  Name string // Name of the monitor
  Enabled bool // Wether the monitor is enabled.
  Run MonitorHandler // The actual handler function.
  GuildOnly bool // Wether this monitor should only run on guilds. (default: false)
  IgnoreWebhooks bool // Wether to ignore messages sent by webhooks (default: true)
  IgnoreBots bool // Wether to ignore messages sent by bots (default: true)
  IgnoreSelf bool // Wether to ignore the bot itself. (default: true)
}

func (m *Monitor) AllowBots() *Monitor {
  m.IgnoreBots = false
  return m
}

func (m *Monitor) AllowWebhooks() *Monitor {
  m.IgnoreWebhooks = false
  return m
}

func (m *Monitor) AllowSelf() *Monitor {
  m.IgnoreSelf = false
  return m
}

func (m *Monitor) SetGuildOnly(toggle bool) *Monitor {
  m.GuildOnly = toggle
  return m
}

func NewMonitor(name string, monitor MonitorHandler) *Monitor {
  return &Monitor{
    Name: name,
    Enabled: true,
    Run: monitor,
    GuildOnly: false,
    IgnoreWebhooks: true,
    IgnoreBots: true,
    IgnoreSelf: true,
  }
}

type MonitorContext struct {
  Message *discordgo.Message
  Channel *discordgo.Channel
  Session *discordgo.Session
  Author *discordgo.User // Alias of Context.Message.Author
  Monitor *Monitor
  Guild *discordgo.Guild
  Bot *Bot
}

func monitorListener(bot *Bot) func(*discordgo.Session, *discordgo.MessageCreate) {
  return func(s *discordgo.Session, m *discordgo.MessageCreate) {
    for _, monitor := range bot.Monitors {
      if !monitor.Enabled {
        continue
      }

      var guild *discordgo.Guild = nil
      if m.GuildID != "" {
        g, err := s.State.Guild(m.GuildID)
        if err != nil {
          continue
        }
        guild = g
      }

      if monitor.GuildOnly && guild == nil {
        continue
      }

      if m.Author.ID == s.State.User.ID && monitor.IgnoreSelf {
        continue
      }

      if m.Author.Bot && monitor.IgnoreBots {
        continue
      }

      if m.WebhookID != "" && monitor.IgnoreWebhooks {
        continue
      }

      channel, err := s.State.Channel(m.ChannelID)
      if err != nil { continue }
      // Discordgo already launched this function in a seperate goroutine we will stay inside it.
      monitor.Run(bot, &MonitorContext{
        Session: s,
        Message: m.Message,
        Author: m.Author,
        Channel: channel,
        Monitor: monitor,
        Guild: guild,
        Bot: bot,
      })
    }
    defer func() {
      if err := recover(); err != nil {
        bot.ErrorHandler(bot, err)
      }
    }()
  }
}

// The regexp used to parse command flags.
// Taken from Klasa https://github.com/dirigeants/klasa
var flagsRegex = regexp.MustCompile("(?:--|—)(\\w[\\w-]+)(?:=(?:[\"]((?:[^\"\\\\]|\\\\.)*)[\"]|[']((?:[^'\\\\]|\\\\.)*)[']|[“”]((?:[^“”\\\\]|\\\\.)*)[“”]|[‘’]((?:[^‘’\\\\]|\\\\.)*)[‘’]|([\\w-]+)))?")
var delim = regexp.MustCompile("(\\s)(?:\\s)+")

// This is the builtin monitor responsible for running commands.
func CommandHandlerMonitor(bot *Bot, ctx *MonitorContext) {
  prefix := bot.Prefix(bot, ctx.Message, ctx.Channel.Type == discordgo.ChannelTypeDM)
  if !strings.HasPrefix(ctx.Message.Content, prefix) {
    return
  }

  // Parsing flags
  // It fills the flags maps and strips them out of the original content.
  flags := make(map[string]string)
  content := strings.Trim(delim.ReplaceAllString(flagsRegex.ReplaceAllStringFunc(ctx.Message.Content, func(m string) string {
    sub := flagsRegex.FindStringSubmatch(m)
    for _, elem := range sub[2:] {
      if elem != "" {
        flags[sub[1]] = elem
        break
      } else {
        flags[sub[1]] = sub[1]
      }
    }
    return ""
  }), "$1"), " ")

  split := strings.Split(content[len(prefix):], " ")

  if len(split) < 1 {
    return
  }

  input := strings.ToLower(split[0])
  var args []string

  if len(split) > 1 {
    args = split[1:]
  }

  cmd := bot.GetCommand(input)
  if cmd == nil {
    return
  }

  // Start constructing a context early so we can call reply and apply the editing rules.
  // Thanks to monitors most of our fields are filled in our monitor context already so we just redirect them.
  cctx := &CommandContext{
    Bot: bot,
    Command: cmd,
    Message: ctx.Message,
    Channel: ctx.Channel,
    Session: ctx.Session,
    Author: ctx.Author,
    RawArgs: args,
    Prefix: prefix,
    Guild: ctx.Guild,
    Flags: flags,
  }

  lang := bot.Language(bot, ctx.Message, ctx.Channel.Type == discordgo.ChannelTypeDM)
  locale, ok := bot.Languages[lang]

  // Shouldn't happen unless the user made a mistake returning an invalid string, let's help them find the problem.
  if !ok {
    fmt.Printf("WARNING: bot.Language handler returned a non-existent language '%s' (command execution aborted)\n", lang)
    return
  }

  // Set the context's locale.
  cctx.Locale = locale

  // Validations.
  if !cmd.Enabled {
    cctx.ReplyLocale("COMMAND_DISABLED")
    return
  }

  if cmd.OwnerOnly && ctx.Author.ID != bot.OwnerID {
    cctx.ReplyLocale("COMMAND_OWNER_ONLY")
    return
  }

  if cmd.GuildOnly && ctx.Message.GuildID == "" {
    cctx.ReplyLocale("COMMAND_GUILD_ONLY")
    return
  }

  // If parse args failed it returns false
  // We don't need to reply since ParseArgs already reports the appropriate error before returning.
  if !cctx.ParseArgs() {
    return
  }

  if bot.CommandTyping {
    ctx.Session.ChannelTyping(ctx.Message.ChannelID)
  }

  canRun, after := bot.CheckCooldown(ctx.Author.ID, cmd.Name, cmd.Cooldown)
  if !canRun {
    cctx.ReplyLocale("COMMAND_COOLDOWN", after)
    return
  }

  bot.CommandsRan++
  cmd.Run(cctx)
}