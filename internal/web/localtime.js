(function () {
  "use strict";
  // The server writes UTC; a viewer whose browser is in UTC gains nothing from a second copy of it.
  var utcZones = ["UTC", "Etc/UTC", "Etc/UCT", "UCT", "Etc/Universal", "Universal", "Etc/Zulu", "Zulu", "GMT", "Etc/GMT"];
  // Only the exact shape the server renders: anything else keeps the server's text.
  var shape = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/;
  function two(n) {
    return (n < 10 ? "0" : "") + n;
  }
  function show() {
    var local;
    try {
      // The locale picks the zone's short name only. The calendar, digits and 24-hour clock are fixed and each
      // field is read from formatToParts, so the date and time keep one shape in every locale.
      local = new Intl.DateTimeFormat(undefined, {
        calendar: "gregory", numberingSystem: "latn", year: "numeric", month: "2-digit", day: "2-digit",
        hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZoneName: "short"
      });
    } catch (e) {
      return;
    }
    var inUTC = utcZones.indexOf(local.resolvedOptions().timeZone) >= 0;
    var times = document.querySelectorAll("time[datetime]");
    for (var i = 0; i < times.length; i++) {
      var value = times[i].getAttribute("datetime");
      if (!value || !shape.test(value)) continue;
      var d = new Date(value);
      if (isNaN(d.getTime())) continue;
      var text = d.getUTCFullYear() + "-" + two(d.getUTCMonth() + 1) + "-" + two(d.getUTCDate()) + " " +
        two(d.getUTCHours()) + ":" + two(d.getUTCMinutes()) + ":" + two(d.getUTCSeconds()) + " UTC";
      if (!inUTC) {
        var p = {};
        local.formatToParts(d).forEach(function (part) { p[part.type] = part.value; });
        text += " (" + p.year + "-" + p.month + "-" + p.day + " " + p.hour + ":" + p.minute + " " + p.timeZoneName + ")";
      }
      times[i].textContent = text;
    }
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", show);
  } else {
    show();
  }
})();
