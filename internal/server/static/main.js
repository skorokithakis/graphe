// Positions each post-it note vertically to align with its anchor in the text,
// then resolves overlaps by pushing later notes downward.
//
// On narrow viewports the post-its are instead moved inline, immediately after
// the block that contains their anchor, so they read like inline footnotes.
// arrangePostits() handles the move; positionPostits() handles desktop layout.
(function () {
  "use strict";

  var GAP_PX = 8; // minimum vertical gap between adjacent post-its
  var MOBILE_QUERY = "(max-width: 700px)";
  // Matches the CSS breakpoint where --margin-width grows. Crossing it
  // reflows post-it text, so cached top values go stale and need recomputing.
  var WIDE_QUERY = "(min-width: 1100px)";

  // Selectors of block-level elements we consider "the block containing the
  // anchor" for inline placement. We prefer the closest ancestor in this list.
  var BLOCK_SELECTOR =
    "p, pre, blockquote, ul, ol, li, h1, h2, h3, h4, h5, h6, table";

  function positionPostits() {
    var marginColumn = document.querySelector(".margin-column");
    if (!marginColumn) return;

    // Skip absolute positioning on narrow viewports where the column is static.
    if (getComputedStyle(marginColumn).position !== "relative") return;

    var columnTop = marginColumn.getBoundingClientRect().top + window.scrollY;

    var postits = Array.from(marginColumn.querySelectorAll(".postit"));

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

  // Move each post-it to its viewport-appropriate location: inline-after-block
  // on narrow viewports, back into the margin column on wide ones. The post-it
  // is moved (not cloned) so event handlers and `<details>` open state survive
  // the relocation.
  function arrangePostits() {
    var marginColumn = document.querySelector(".margin-column");
    if (!marginColumn) return;

    var isMobile = window.matchMedia(MOBILE_QUERY).matches;
    var postits = Array.from(document.querySelectorAll(".postit"));

    if (!isMobile) {
      // Desktop: ensure all post-its live in the margin column. The original
      // server-rendered order is preserved by appendChild's natural ordering
      // among already-attached children; freshly returning ones go to the end.
      postits.forEach(function (postit) {
        if (postit.parentElement !== marginColumn) {
          marginColumn.appendChild(postit);
        }
      });
      positionPostits();
      return;
    }

    // Mobile: move each post-it to immediately after its anchor's block.
    postits.forEach(function (postit) {
      var id = postit.dataset.commentId;
      var anchor = document.querySelector(
        'mark.anchor[data-comment-id="' + id + '"]'
      );
      if (!anchor) return; // leave it in margin column as a fallback.

      var block = anchor.closest(BLOCK_SELECTOR);
      if (!block) return;

      // If multiple comments anchor in the same block, the inserts would
      // otherwise reverse their order. Walk past any post-its already attached
      // after this block so we land at the end of that run.
      var insertAfter = block;
      while (
        insertAfter.nextElementSibling &&
        insertAfter.nextElementSibling.classList.contains("postit")
      ) {
        insertAfter = insertAfter.nextElementSibling;
      }
      insertAfter.parentNode.insertBefore(postit, insertAfter.nextSibling);

      // Drop any stale `top` set by an earlier desktop layout.
      postit.style.top = "";
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

  // Re-run desktop layout when a post-it expands or collapses, so neighbours
  // float up to fill the gap (or get pushed down to make room). The `toggle`
  // event does not bubble, so we attach a listener to each <details> directly.
  // Post-its are moved (not recreated) between margin column and inline
  // positions on viewport changes, so wiring once at load is enough.
  // positionPostits() early-returns on mobile, so the handler is a no-op there.
  function wireToggleReposition() {
    document.querySelectorAll(".postit").forEach(function (postit) {
      postit.addEventListener("toggle", positionPostits);
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    arrangePostits();
    wireHover();
    wireToggleReposition();
    connectSSE();

    // Re-arrange when the breakpoint is crossed (e.g. device rotation,
    // window resize). Using matchMedia avoids re-running on every resize tick.
    window.matchMedia(MOBILE_QUERY).addEventListener("change", arrangePostits);
    window.matchMedia(WIDE_QUERY).addEventListener("change", positionPostits);
  });
})();
