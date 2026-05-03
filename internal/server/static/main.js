// Positions each post-it note vertically to align with its anchor in the text,
// then resolves overlaps by pushing later notes downward.
//
// This runs once on DOMContentLoaded. The margin column uses position:relative
// so each post-it's `top` is relative to the column's top edge, which in turn
// is aligned with the start of the post body via CSS grid row alignment.
(function () {
  "use strict";

  var GAP_PX = 8; // minimum vertical gap between adjacent post-its

  function positionPostits() {
    var marginColumn = document.querySelector(".margin-column");
    if (!marginColumn) return;

    // Skip absolute positioning on narrow viewports where the column is static.
    if (getComputedStyle(marginColumn).position !== "relative") return;

    var columnTop = marginColumn.getBoundingClientRect().top + window.scrollY;

    var postits = Array.from(document.querySelectorAll(".postit"));

    // Build a list of {postit, desiredTop} sorted by desiredTop ascending.
    var entries = postits
      .map(function (postit) {
        var id = postit.dataset.commentId;
        var anchor = document.querySelector(
          'mark.anchor[data-comment-id="' + id + '"]'
        );
        var desiredTop = 0;
        if (anchor) {
          var anchorRect = anchor.getBoundingClientRect();
          desiredTop =
            anchorRect.top + window.scrollY - columnTop;
        }
        return { postit: postit, desiredTop: desiredTop };
      })
      .sort(function (a, b) {
        return a.desiredTop - b.desiredTop;
      });

    // Place each post-it, pushing it down if it would overlap the previous one.
    var nextAvailableTop = 0;
    entries.forEach(function (entry) {
      var top = Math.max(entry.desiredTop, nextAvailableTop);
      entry.postit.style.top = top + "px";
      nextAvailableTop = top + entry.postit.offsetHeight + GAP_PX;
    });
  }

  // Wire up hover cross-highlighting between anchors and their post-its.
  function wireHover() {
    document.querySelectorAll("mark.anchor").forEach(function (anchor) {
      var id = anchor.dataset.commentId;
      var postit = document.querySelector(
        '.postit[data-comment-id="' + id + '"]'
      );
      if (!postit) return;

      function activate() {
        anchor.classList.add("active");
        postit.classList.add("active");
      }
      function deactivate() {
        anchor.classList.remove("active");
        postit.classList.remove("active");
      }

      anchor.addEventListener("mouseenter", activate);
      anchor.addEventListener("mouseleave", deactivate);
      postit.addEventListener("mouseenter", activate);
      postit.addEventListener("mouseleave", deactivate);
    });
  }

  // SSE connection for live reload. The server stubs this out with a heartbeat;
  // the live-reload ticket (gra-dmtfx) will send real "reload" events.
  function connectSSE() {
    var source = new EventSource("/events");
    source.addEventListener("reload", function () {
      window.location.reload();
    });
    // Reconnect automatically on error (browser does this by default for
    // EventSource, but we log it for visibility during development).
    source.addEventListener("error", function () {
      console.warn("SSE connection lost, browser will retry.");
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    positionPostits();
    wireHover();
    connectSSE();
  });
})();
