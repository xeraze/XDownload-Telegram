require "telegram/bot"
require "json"
require "tmpdir"
require "fileutils"
require "open3"
require "securerandom"
require "rbconfig"
require "uri"

TOKEN = ENV.fetch("XDL_BOT_TOKEN") { abort "Set XDL_BOT_TOKEN environment variable" }
BOT_API_URL = ENV["XDL_BOT_API"] || "https://api.telegram.org"

START_EN = <<~TEXT.freeze
  Hi! I'm XDownload — paste a link, pick a format, get the file.

  Works in chats and groups.
  /help — what I support
TEXT

START_RU = <<~TEXT.freeze
  Привет! Я XDownload — вставьте ссылку, выберите формат, получите файл.

  Работаю в чатах и группах.
  /help — что я поддерживаю
TEXT

HELP_EN = <<~TEXT.freeze
  How it works
  1. Send a media link
  2. Choose Audio (mp3) or Video (mp4)
  3. For video pick quality: 480p/720p/1080p
  4. Get the file in the chat

  Supported
  • YouTube
  • YouTube Music
  • Spotify
  • SoundCloud
  • TikTok, Instagram, Facebook
  • Reddit, Pinterest, VK
  • X (Twitter), Rumble
  • Snapchat

  Notes
  • Spotify & SoundCloud — audio only
  • One link every 5 seconds (anti-spam)
  • Buttons (format / quality) have no cooldown
  • Private / 18+ / region-locked media may not be downloadable
  • Telegram limits file size on public bots
TEXT

HELP_RU = <<~TEXT.freeze
  Как это работает
  1. Отправьте ссылку на медиа
  2. Выберите Аудио (mp3) или Видео (mp4)
  3. Для видео выберите качество: 480p/720p/1080p
  4. Получите файл в чате

  Поддерживается
  • YouTube
  • YouTube Music
  • Spotify
  • SoundCloud
  • TikTok, Instagram, Facebook
  • Reddit, Pinterest, VK
  • X (Twitter), Rumble
  • Snapchat

  Примечания
  • Spotify и SoundCloud — только аудио
  • Одна ссылка раз в 5 секунд (анти-спам)
  • Кнопки (формат / качество) без кулдауна
  • Приватный / 18+ / региональный контент может не скачиваться
  • Telegram ограничивает размер файла в публичных ботах
TEXT

STR = {
  en: {
    start: START_EN,
    help: HELP_EN,
    expired: "Request expired, send the link again.",
    pick_quality: "Pick a video quality:",
    downloading_audio: "Downloading audio...",
    downloading_video: "Downloading video (%{h}p)...",
    unexpected: "Unexpected error: %{msg}",
    busy: "Download is already running — wait for the file.",
    err_tools: "Download tool isn't installed on the server.",
    err_ffmpeg: "The server is missing ffmpeg — can't process this file.",
    err_spotify: "Couldn't read this Spotify link — try again in a minute.",
    err_link: "This link isn't supported.",
    err_private: "This video is private, region-locked, or no longer available.",
    err_signin: "This video requires sign-in or is age-restricted.",
    err_403: "YouTube rejected the download (403). Wait a minute and try again.",
    err_track: "This track isn't available to download right now — YouTube Music has no match for it.",
    err_session: "Couldn't reach Spotify to resolve this track — try again in a minute.",
    err_spotdl: "YouTube didn't respond — try again in a minute.",
    err_default: "Download failed. Try a different link, or make sure yt-dlp and ffmpeg are installed.",
    cooldown: "Slow down — wait %{s}s before sending another link.",
    got_audio: "Got it! This platform is audio only.",
    got_format: "Got it! Pick a format:",
    need_link: "I need a media link.",
    not_supported: "This platform isn't supported.\nSend /help to see the list.",
    file_missing: "Download finished but the file is missing.",
    too_big: "This file is over %{mb} MB even at the lowest quality — can't send it.",
    lowered: "The video was over %{mb} MB, so I lowered the quality to %{h}p.",
    audio_btn: "🎵 Audio (mp3)",
    video_btn: "🎬 Video (mp4)",
    lang_pick: "Choose a language:",
    lang_set: "Language: English.",
    pause_today: "Scheduled shutdown — the bot opens today at 09:00.",
    pause_tomorrow: "Scheduled shutdown — the bot opens tomorrow at 09:00.",
    err_technical: "Technical difficulties on the server — try again later.",
    announce_header: "Developer announcement",
    announce_usage: "Usage: /notice <text>",
    unknown_cmd: "Unknown command. Try /help.",
    announce_sent: "Sent to %{n} chats."
  },
  ru: {
    start: START_RU,
    help: HELP_RU,
    expired: "Запрос устарел, отправьте ссылку снова.",
    pick_quality: "Выберите качество видео:",
    downloading_audio: "Скачиваю аудио...",
    downloading_video: "Скачиваю видео (%{h}p)...",
    unexpected: "Непредвиденная ошибка: %{msg}",
    busy: "Загрузка уже идёт — дождитесь файла.",
    err_tools: "На сервере не установлен инструмент загрузки.",
    err_ffmpeg: "На сервере отсутствует ffmpeg — не могу обработать файл.",
    err_spotify: "Не удалось прочитать эту ссылку Spotify — попробуйте через минуту.",
    err_link: "Эта ссылка не поддерживается.",
    err_private: "Видео приватное, недоступно в вашем регионе или удалено.",
    err_signin: "Видео требует входа в аккаунт или предназначено для взрослых.",
    err_403: "YouTube отклонил загрузку (403). Подождите минуту и попробуйте снова.",
    err_track: "Трек сейчас недоступен для скачивания — YouTube Music не нашёл совпадений.",
    err_session: "Не удалось связаться с Spotify — попробуйте через минуту.",
    err_spotdl: "YouTube не отвечает — попробуйте через минуту.",
    err_default: "Не удалось скачать. Попробуйте другую ссылку или проверьте, что yt-dlp и ffmpeg установлены.",
    cooldown: "Слишком часто — подождите %{s}s перед следующей ссылкой.",
    got_audio: "Принято! Эта платформа только для аудио.",
    got_format: "Принято! Выберите формат:",
    need_link: "Мне нужна ссылка на медиа.",
    not_supported: "Эта платформа не поддерживается.\nОтправьте /help, чтобы увидеть список.",
    file_missing: "Загрузка завершена, но файла нет.",
    too_big: "Файл больше %{mb} МБ даже в минимальном качестве — не могу отправить.",
    lowered: "Видео было больше %{mb} МБ, поэтому снизил качество до %{h}p.",
    audio_btn: "🎵 Аудио (mp3)",
    video_btn: "🎬 Видео (mp4)",
    lang_pick: "Выберите язык:",
    lang_set: "Язык: русский.",
    pause_today: "Плановое выключение — бот откроется сегодня в 09:00.",
    pause_tomorrow: "Плановое выключение — бот откроется завтра в 09:00.",
    err_technical: "Технические неполадки на сервере — попробуйте позже.",
    announce_header: "Уведомление от разработчика",
    announce_usage: "Использование: /notice <текст>",
    unknown_cmd: "Неизвестная команда. Попробуйте /help.",
    announce_sent: "Отправлено в %{n} чатов."
  }
}.freeze

LANG_FILE = File.join(Dir.tmpdir, "xdl-lang.txt")
LANG_CACHE = {}
LANG_MUTEX = Mutex.new

def load_langs
  File.readlines(LANG_FILE, chomp: true).each do |line|
    id, code = line.split("=", 2)
    next unless id&.match?(/\A\d+\z/) && %w[en ru].include?(code)

    LANG_CACHE[id.to_i] = code
  end
rescue StandardError
  nil
end

def lang_of(chat_id)
  LANG_MUTEX.synchronize { LANG_CACHE[chat_id] } || "en"
end

def set_lang(chat_id, code)
  lines = LANG_MUTEX.synchronize do
    LANG_CACHE[chat_id] = code
    LANG_CACHE.map { |k, v| "#{k}=#{v}" }
  end
  File.write(LANG_FILE, lines.join("\n"))
rescue StandardError
  nil
end

def t(chat_id, key, vars = {})
  s = STR[lang_of(chat_id).to_sym][key] || STR[:en][key] || key.to_s
  vars.empty? ? s : format(s, vars)
end

load_langs

CORE = ENV["XDL_CORE"] || begin
  base = File.expand_path("../core", __dir__)
  exe = RbConfig::CONFIG["host_os"] =~ /mswin|mingw|cygwin/ ? "xcore.exe" : "xcore"
  File.join(base, exe)
end

CHATS_FILE = File.join(Dir.tmpdir, "xdl-chats.txt")
ADMIN_ID = 8412276336

WORKDIR = ENV["XDL_TMP"] || File.join(Dir.tmpdir, "xdownload")

FileUtils.mkdir_p(WORKDIR)
Dir.glob(File.join(WORKDIR, "*")).each { |f| FileUtils.rm_rf(f) }
MAX_MB = if ENV["XDL_MAX_MB"] && !ENV["XDL_MAX_MB"].empty?
  ENV["XDL_MAX_MB"].to_i
elsif BOT_API_URL =~ %r{127\.0\.0\.1|localhost|192\.168\.}
  2000
else
  49
end

PENDING = {}
PENDING_MUTEX = Mutex.new

COOLDOWN_SEC = Integer(ENV.fetch("XDL_COOLDOWN_SEC", "5"))
LAST_ACTION = {}
LAST_ACTION_MUTEX = Mutex.new
USER_BUSY = {}
USER_BUSY_MUTEX = Mutex.new

ALLOWED_HOSTS = %w[
  youtube.com
  youtu.be
  music.youtube.com
  open.spotify.com
  spotify.com
  soundcloud.com
  tiktok.com
  instagram.com
  facebook.com
  fb.watch
  reddit.com
  redd.it
  pin.it
  vk.com
  x.com
  twitter.com
  rumble.com
  snapchat.com
  snap.com
  t.co
].freeze

def allowed_url?(url)
  return true if url.start_with?("spotify:")

  host = begin
    URI.parse(url).host
  rescue URI::InvalidURIError
    nil
  end
  return false if host.nil? || host.empty?

  host = host.downcase
  return true if ALLOWED_HOSTS.any? { |h| host == h || host.end_with?(".#{h}") }
  bare = host.delete_prefix("www.")
  !!bare.match?(/\Apinterest\.[a-z]{2,3}\z/)
end

def cooldown_left(user_id)
  return 0 if COOLDOWN_SEC <= 0
  now = Time.now.to_f
  last = LAST_ACTION_MUTEX.synchronize { LAST_ACTION[user_id] }
  return 0 unless last
  left = COOLDOWN_SEC - (now - last)
  left.positive? ? left.ceil : 0
end

def touch_cooldown(user_id)
  return if COOLDOWN_SEC <= 0
  LAST_ACTION_MUTEX.synchronize do
    LAST_ACTION[user_id] = Time.now.to_f
    if LAST_ACTION.size > 10_000
      cutoff = Time.now.to_f - (COOLDOWN_SEC * 4)
      LAST_ACTION.delete_if { |_k, v| v < cutoff }
    end
  end
end

def user_busy?(user_id)
  USER_BUSY_MUTEX.synchronize { !!USER_BUSY[user_id] }
end

def set_user_busy(user_id, value)
  USER_BUSY_MUTEX.synchronize do
    if value
      USER_BUSY[user_id] = true
    else
      USER_BUSY.delete(user_id)
    end
  end
end

def begin_download(user_id)
  return nil unless user_id
  return :busy if user_busy?(user_id)

  set_user_busy(user_id, true)
  nil
end

def end_download(user_id)
  return unless user_id
  set_user_busy(user_id, false)
end

def spotify_url?(text)
  text.include?("open.spotify.com") || text.start_with?("spotify:")
end

def soundcloud_url?(text)
  text.include?("soundcloud.com")
end

def audio_only_url?(text)
  spotify_url?(text) || soundcloud_url?(text)
end

def extract_url(text)
  text[/https?:\/\/\S+/] || text[/spotify:[^\s]+/]
end

def core(*args)
  out, err, status = Open3.capture3(CORE, *args)
  [out, err, status.success?]
end

def track_info(url)
  return { "title" => "Spotify track", "is_spotify" => true } if spotify_url?(url)

  out, err, ok = core("info", url)
  return nil unless ok

  JSON.parse(out)
rescue JSON::ParserError
  nil
end

def safe_caption(text, max = 120)
  text.to_s.strip.gsub(/[\u0000-\u001f]/, "").byteslice(0, max) || ""
end

def friendly_error(chat_id, err)
  e = err.to_s
  key = case e
        when /no such file|not found.*yt-dlp|yt-dlp.*not install|spotdl.*not/i then :err_tools
        when /connection refused|ECONNREFUSED|Failed to connect|127\.0\.0\.1|localhost:\d+/i then :err_technical
        when /ffmpeg/i then :err_ffmpeg
        when /could not fetch track info/i then :err_spotify
        when /unsupported URL/i then :err_link
        when /private|unavailable|removed|no longer/i then :err_private
        when /sign in|age|restricted/i then :err_signin
        when /403|Forbidden|po.?token/i then :err_403
        when /no output file was found|track unavailable/i then :err_track
        when /Could not get session/i then :err_session
        when /spotdl failed/i then :err_spotdl
        else :err_default
        end
  t(chat_id, key)
end

def format_buttons(chat_id, url, audio_only)
  token = SecureRandom.hex(8)
  PENDING_MUTEX.synchronize { PENDING[token] = url }

  audio = Telegram::Bot::Types::InlineKeyboardButton.new(text: t(chat_id, :audio_btn), callback_data: "dl:#{token}:mp3")
  buttons = [audio]
  unless audio_only
    buttons << Telegram::Bot::Types::InlineKeyboardButton.new(text: t(chat_id, :video_btn), callback_data: "dl:#{token}:mp4")
  end
  Telegram::Bot::Types::InlineKeyboardMarkup.new(inline_keyboard: [buttons])
end

def handle_callback(bot, cb)
  data = cb.data.to_s
  case data
  when /\Adl:/ then handle_format_choice(bot, cb, data)
  when /\Aqv:/ then handle_quality_choice(bot, cb, data)
  when /\Alang:/ then handle_lang_choice(bot, cb, data)
  end
end

def handle_lang_choice(bot, cb, data)
  _cmd, code = data.split(":")
  return unless %w[en ru].include?(code)

  chat_id = cb.message.chat.id
  set_lang(chat_id, code)
  bot.api.answer_callback_query(callback_query_id: cb.id)
  bot.api.edit_message_text(
    chat_id: chat_id,
    message_id: cb.message.message_id,
    text: t(chat_id, :lang_set),
    reply_markup: Telegram::Bot::Types::InlineKeyboardMarkup.new(inline_keyboard: [])
  )
end

def handle_format_choice(bot, cb, data)
  _cmd, token, ext = data.split(":")
  chat_id = cb.message.chat.id

  if (pause = night_pause(chat_id))
    bot.api.answer_callback_query(callback_query_id: cb.id, text: pause)
    return
  end

  if ext == "mp4"
    url = PENDING_MUTEX.synchronize { PENDING[token] }
    if url.nil?
      bot.api.answer_callback_query(callback_query_id: cb.id, text: t(chat_id, :expired))
      return
    end
    bot.api.answer_callback_query(callback_query_id: cb.id)
    buttons = [480, 720, 1080].map do |h|
      Telegram::Bot::Types::InlineKeyboardButton.new(text: "#{h}p", callback_data: "qv:#{token}:#{h}")
    end
    bot.api.send_message(
      chat_id: chat_id,
      text: t(chat_id, :pick_quality),
      reply_markup: Telegram::Bot::Types::InlineKeyboardMarkup.new(inline_keyboard: [buttons])
    )
    return
  end

  url = PENDING_MUTEX.synchronize { PENDING.delete(token) }
  if url.nil?
    bot.api.answer_callback_query(callback_query_id: cb.id, text: t(chat_id, :expired))
    return
  end

  user_id = cb.from&.id
  if (msg = begin_download(user_id))
    bot.api.answer_callback_query(callback_query_id: cb.id, text: t(chat_id, msg))
    PENDING_MUTEX.synchronize { PENDING[token] = url }
    return
  end

  bot.api.answer_callback_query(callback_query_id: cb.id)
  bot.api.send_message(chat_id: chat_id, text: t(chat_id, :downloading_audio))

  Thread.new do
    begin
      send_file(bot, chat_id, url, "mp3")
    rescue StandardError => e
      bot.api.send_message(chat_id: chat_id, text: t(chat_id, :unexpected, msg: e.message))
    ensure
      end_download(user_id)
    end
  end
end

def handle_quality_choice(bot, cb, data)
  _cmd, token, height = data.split(":")
  chat_id = cb.message.chat.id

  if (pause = night_pause(chat_id))
    bot.api.answer_callback_query(callback_query_id: cb.id, text: pause)
    return
  end

  url = PENDING_MUTEX.synchronize { PENDING.delete(token) }
  if url.nil?
    bot.api.answer_callback_query(callback_query_id: cb.id, text: t(chat_id, :expired))
    return
  end

  user_id = cb.from&.id
  if (msg = begin_download(user_id))
    bot.api.answer_callback_query(callback_query_id: cb.id, text: t(chat_id, msg))
    PENDING_MUTEX.synchronize { PENDING[token] = url }
    return
  end

  bot.api.answer_callback_query(callback_query_id: cb.id)
  bot.api.send_message(chat_id: chat_id, text: t(chat_id, :downloading_video, h: height))

  Thread.new do
    begin
      send_file(bot, chat_id, url, "mp4", height.to_i)
    rescue StandardError => e
      bot.api.send_message(chat_id: chat_id, text: t(chat_id, :unexpected, msg: e.message))
    ensure
      end_download(user_id)
    end
  end
end

def send_file(bot, chat_id, url, ext, height = 1080)
  ext = "mp3" if ext == "mp4" && audio_only_url?(url)

  info = track_info(url)
  title = safe_caption(info && info["title"])
  FileUtils.mkdir_p(WORKDIR)

  is_video = ext == "mp4"
  bot.api.send_chat_action(chat_id: chat_id, action: is_video ? "upload_video" : "upload_document")

  heights = is_video ? [height, 720, 480].uniq : [nil]
  path = nil
  sent = nil

  heights.each do |h|
    args = ["dl", "--dir", WORKDIR, "--ext", ext]
    args += ["--height", h.to_s] if h
    out, err, ok = core(*args, url)
    unless ok
      bot.logger.error("core failed args=#{args.inspect} stderr=#{err}")
      bot.api.send_message(chat_id: chat_id, text: friendly_error(chat_id, err.to_s))
      return
    end

    out.force_encoding(Encoding::BINARY).lines.reverse_each do |line|
      if line.start_with?("RESULT:")
        path = line.sub("RESULT:", "").strip.force_encoding(Encoding::UTF_8)
        break
      end
    end
    if path.nil? || !File.file?(path)
      bot.api.send_message(chat_id: chat_id, text: t(chat_id, :file_missing))
      return
    end

    mb = File.size(path) / 1024.0 / 1024.0
    if mb <= MAX_MB
      sent = h
      break
    end

    FileUtils.rm_f(path)
    path = nil
  end

  if path.nil?
    bot.api.send_message(chat_id: chat_id, text: t(chat_id, :too_big, mb: MAX_MB))
    return
  end

  if is_video && sent && sent != height
    bot.api.send_message(chat_id: chat_id, text: t(chat_id, :lowered, mb: MAX_MB, h: sent))
  end

  if is_video
    bot.api.send_video(
      chat_id: chat_id,
      video: Faraday::UploadIO.new(path, "video/mp4"),
      caption: title,
      supports_streaming: true
    )
  else
    bot.api.send_audio(
      chat_id: chat_id,
      audio: Faraday::UploadIO.new(path, "audio/mpeg"),
      caption: title
    )
  end
ensure
  if path && File.file?(path)
    3.times do
      FileUtils.rm_f(path)
      break unless File.file?(path)
      sleep 0.3
    end
  end
end

def known_chats
  File.readlines(CHATS_FILE, chomp: true).filter_map { |l| Integer(l, exception: false) }.uniq
rescue StandardError
  []
end

def record_chat(chat_id)
  return unless chat_id
  return if known_chats.include?(chat_id)

  File.open(CHATS_FILE, "a") { |f| f.puts(chat_id) }
rescue StandardError
  nil
end

def utf16_len(str)
  str.each_char.sum { |c| c.ord > 0xFFFF ? 2 : 1 }
end

def night_pause(chat_id)
  now = Time.now
  minutes = now.hour * 60 + now.min
  close_min = [5, 6].include?(now.wday) ? 21 * 60 + 30 : 21 * 60
  return nil if minutes >= 9 * 60 && minutes < close_min

  t(chat_id, minutes < 9 * 60 ? :pause_today : :pause_tomorrow)
end

def broadcast_notice(bot, body)
  body = body.strip.gsub(/\n{2,}/, "\n")
  sent = 0
  known_chats.each do |cid|
    header = t(cid, :announce_header)
    text = "#{header}\n#{body}"
    entities = [
      { type: "bold", offset: 0, length: utf16_len(header) },
      { type: "blockquote", offset: utf16_len(header) + 1, length: utf16_len(body) }
    ]
    bot.api.send_message(chat_id: cid, text: text, entities: entities)
    sent += 1
  rescue StandardError => e
    bot.logger.warn("notice to #{cid} failed: #{e.message}")
  end
  sent
end

Telegram::Bot::Client.run(TOKEN, url: BOT_API_URL) do |bot|
  bot.logger = Logger.new($stderr)
  bot.logger.level = Logger::INFO
  bot.logger.info("XDownload bot started")
  begin
    bot.api.set_my_commands(
      commands: [
        { command: "start", description: "Start the bot" },
        { command: "help", description: "How it works" },
        { command: "lang", description: "Change language" }
      ]
    )
    bot.api.set_my_commands(
      language_code: "en",
      commands: [
        { command: "start", description: "Start the bot" },
        { command: "help", description: "How it works" },
        { command: "lang", description: "Change language" }
      ]
    )
    bot.api.set_my_commands(
      language_code: "ru",
      commands: [
        { command: "start", description: "Запустить бота" },
        { command: "help", description: "Как это работает" },
        { command: "lang", description: "Сменить язык" }
      ]
    )
    bot.logger.info("set_my_commands ok")
  rescue StandardError => e
    bot.logger.warn("set_my_commands failed: #{e.message}")
  end
  bot.listen do |update|
    case update
    when Telegram::Bot::Types::CallbackQuery
      record_chat(update.message&.chat&.id)
      handle_callback(bot, update)
    when Telegram::Bot::Types::Message
      record_chat(update.chat.id)
      next if update.text.nil?

      chat_id = update.chat.id
      text = update.text.strip
      case text
      when "/start"
        bot.api.send_message(chat_id: chat_id, text: t(chat_id, :start))
      when "/help"
        bot.api.send_message(chat_id: chat_id, text: t(chat_id, :help))
      when "/lang"
        kb = Telegram::Bot::Types::InlineKeyboardMarkup.new(
          inline_keyboard: [[
            Telegram::Bot::Types::InlineKeyboardButton.new(text: "Русский", callback_data: "lang:ru"),
            Telegram::Bot::Types::InlineKeyboardButton.new(text: "English", callback_data: "lang:en")
          ]]
        )
        bot.api.send_message(chat_id: chat_id, text: t(chat_id, :lang_pick), reply_markup: kb)
      when %r{\A/notice}
        if update.from&.id == ADMIN_ID
          body = text.sub(%r{\A/notice\s*}, "")
          if body.empty?
            bot.api.send_message(chat_id: chat_id, text: t(chat_id, :announce_usage))
          else
            n = broadcast_notice(bot, body)
            bot.api.send_message(chat_id: chat_id, text: t(chat_id, :announce_sent, n: n))
          end
        else
          bot.api.send_message(chat_id: chat_id, text: t(chat_id, :unknown_cmd))
        end
      else
        url = extract_url(text)
        if url.nil?
          bot.api.send_message(chat_id: chat_id, text: t(chat_id, :need_link))
          next
        end
        unless allowed_url?(url)
          bot.api.send_message(chat_id: chat_id, text: t(chat_id, :not_supported))
          next
        end
        if (pause = night_pause(chat_id))
          bot.api.send_message(chat_id: chat_id, text: pause)
          next
        end
        user_id = update.from&.id
        if user_id && (left = cooldown_left(user_id)) > 0
          bot.api.send_message(chat_id: chat_id, text: t(chat_id, :cooldown, s: left))
          next
        end
        touch_cooldown(user_id) if user_id
        audio_only = audio_only_url?(url)
        bot.api.send_message(
          chat_id: chat_id,
          text: t(chat_id, audio_only ? :got_audio : :got_format),
          reply_markup: format_buttons(chat_id, url, audio_only)
        )
      end
    end
  end
end