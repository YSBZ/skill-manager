"use strict";

(() => {
  function safeHTTPURL(raw) {
    try {
      const url = new URL(String(raw || ""));
      if ((url.protocol === "http:" || url.protocol === "https:") && url.host && !url.username && !url.password) return url.href;
    } catch { /* invalid or relative upstream links are omitted */ }
    return "";
  }

  function parseDate(raw) {
    if (typeof raw !== "string") return null;
    const match = /^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.\d+)?(Z|([+-])(\d\d):(\d\d))$/.exec(raw);
    if (!match) return null;
    const year = Number(match[1]);
    const month = Number(match[2]);
    const day = Number(match[3]);
    const hour = Number(match[4]);
    const minute = Number(match[5]);
    const second = Number(match[6]);
    const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    const monthDays = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
    if (month < 1 || month > 12 || day < 1 || day > monthDays[month - 1] || hour > 23 || minute > 59 || second > 59) return null;
    if (match[9] && (Number(match[9]) > 23 || Number(match[10]) > 59)) return null;
    const date = new Date(raw);
    return Number.isFinite(date.getTime()) ? date : null;
  }

  function sourceIconName(source) {
    const name = source && typeof source.name === "string" ? source.name : "";
    const url = safeHTTPURL(source && source.url);
    let hostname = "";
    try { hostname = new URL(url).hostname.toLowerCase().replace(/^www\./, ""); }
    catch { /* absent or invalid source URLs use the generic source glyph */ }
    const matchesDomain = (domain) => hostname === domain || hostname.endsWith("." + domain);

    if (matchesDomain("github.com") || matchesDomain("github.blog")) return "git-branch";
    if (matchesDomain("x.com") || matchesDomain("twitter.com")) return "at-sign";
    if (matchesDomain("youtube.com") || matchesDomain("youtube-nocookie.com") || matchesDomain("youtu.be")) return "play";
    if (matchesDomain("arxiv.org")) return "file-text";
    if (/rss/i.test(name)) return "rss";
    return "globe";
  }

  window.SkillManagerContentUI = Object.freeze({ safeHTTPURL, parseDate, sourceIconName });
})();
