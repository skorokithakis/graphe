// Positions each post-it note vertically to align with its anchor in the text,
// then resolves overlaps by pushing later notes downward.
//
// On narrow viewports the post-its are instead moved inline, immediately after
// the block that contains their anchor, so they read like inline footnotes.
// arrangePostits() handles the move; positionPostits() handles desktop layout.
(function () {
  "use strict";

  // Apply collapsed state from localStorage before any layout pass so that
  // post-its measure at their collapsed height on the first arrange. The doc id
  // scopes the key per document so random c-xxxx IDs from different files do
  // not bleed into each other. The try/catch matches the FOUC theme script
  // style: a storage exception must not break the page.
  (function () {
    try {
      var docID = document.documentElement.getAttribute("data-graphe-doc-id");
      if (!docID) return;
      var stored = localStorage.getItem("graphe-collapsed-postits:" + docID);
      if (!stored) return;
      var collapsed = JSON.parse(stored);
      if (!Array.isArray(collapsed)) return;
      var collapsedSet = {};
      collapsed.forEach(function (id) { collapsedSet[id] = true; });
      document.querySelectorAll("details.postit[data-comment-id]").forEach(function (postit) {
        if (collapsedSet[postit.dataset.commentId]) {
          postit.removeAttribute("open");
        }
      });
    } catch (e) {}
  }());

  var THEME_MODES = ["auto", "light", "dark"];
  var THEME_STORAGE_KEY = "graphe-theme";
  var THEME_GLYPH = { auto: "◐", light: "☀", dark: "☾" };

  // Tracks the active matchMedia listener so it can be removed when leaving
  // auto mode. null means no listener is currently attached.
  var mediaQueryListener = null;

  function applyTheme(mode) {
    var resolved;
    if (mode === "light" || mode === "dark") {
      resolved = mode;
    } else {
      resolved = window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light";
    }
    document.documentElement.setAttribute("data-theme", resolved);
  }

  function readStoredMode() {
    var stored = localStorage.getItem(THEME_STORAGE_KEY);
    if (stored === "light" || stored === "dark") return stored;
    return "auto";
  }

  function syncToggleButton(mode) {
    var button = document.querySelector(".theme-toggle");
    if (!button) return;
    button.textContent = THEME_GLYPH[mode];
    button.setAttribute("aria-label", "Theme: " + mode);
  }

  // Adds the matchMedia listener when active is true, removes it when false.
  // Guards against double-add (active=true when already set) and no-op remove.
  function setAutoListener(active) {
    var mq = window.matchMedia("(prefers-color-scheme: dark)");
    if (active && !mediaQueryListener) {
      mediaQueryListener = function () { applyTheme("auto"); };
      mq.addEventListener("change", mediaQueryListener);
    } else if (!active && mediaQueryListener) {
      mq.removeEventListener("change", mediaQueryListener);
      mediaQueryListener = null;
    }
  }

  // Sync the button glyph and aria-label immediately — the script is at the
  // end of body so the button is already in the DOM. The FOUC script set
  // data-theme-mode on <html> so we don't need to re-read localStorage here.
  (function () {
    var mode = document.documentElement.getAttribute("data-theme-mode") || "auto";
    syncToggleButton(mode);
    setAutoListener(mode === "auto");
  }());

  function wireThemeToggle() {
    var button = document.querySelector(".theme-toggle");
    if (!button) return;
    button.addEventListener("click", function () {
      var current = readStoredMode();
      var nextMode = THEME_MODES[(THEME_MODES.indexOf(current) + 1) % THEME_MODES.length];

      if (nextMode === "auto") {
        // Removing the key rather than storing "auto" avoids a stale value if
        // the user clears storage manually between visits.
        localStorage.removeItem(THEME_STORAGE_KEY);
      } else {
        localStorage.setItem(THEME_STORAGE_KEY, nextMode);
      }
      // Keep data-theme-mode in sync so re-renders (SSE reload) can read the
      // current mode without touching localStorage again.
      document.documentElement.setAttribute("data-theme-mode", nextMode);

      applyTheme(nextMode);
      syncToggleButton(nextMode);
      setAutoListener(nextMode === "auto");
    });
  }

  var GAP_PX = 8; // minimum vertical gap between adjacent post-its
  var MOBILE_QUERY = "(max-width: 700px)";
  // Matches the CSS breakpoint where --margin-width grows. Crossing it
  // reflows post-it text, so cached top values go stale and need recomputing.
  var WIDE_QUERY = "(min-width: 1100px)";

  // Selectors of block-level elements we consider "the block containing the
  // anchor" for inline placement. We prefer the closest ancestor in this list.
  var BLOCK_SELECTOR =
    "p, pre, blockquote, ul, ol, li, h1, h2, h3, h4, h5, h6, table";

  // findAnchorElement returns the DOM element that marks the anchor position
  // for a given comment ID. For normal comments this is a mark.anchor element;
  // for orphaned comments it falls back to the zero-width span.orphan-pin
  // inserted by the server.
  function findAnchorElement(id) {
    return (
      document.querySelector('mark.anchor[data-comment-id="' + id + '"]') ||
      document.querySelector('span.orphan-pin[data-comment-id="' + id + '"]')
    );
  }

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
        var anchor = findAnchorElement(id);
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

    updatePageNav();
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
      var anchor = findAnchorElement(id);
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

    updatePageNav();
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

  // Persist the collapsed state of each post-it to localStorage so it survives
  // browser reloads and SSE-driven reloads. The try/catch matches the FOUC
  // theme script style: a storage exception must not break the page.
  function wireToggleCollapse() {
    var docID = document.documentElement.getAttribute("data-graphe-doc-id");
    if (!docID) return;
    var storageKey = "graphe-collapsed-postits:" + docID;

    document.querySelectorAll(".postit").forEach(function (postit) {
      postit.addEventListener("toggle", function () {
        try {
          var stored = localStorage.getItem(storageKey);
          var collapsed = (stored && JSON.parse(stored));
          if (!Array.isArray(collapsed)) collapsed = [];
          var id = postit.dataset.commentId;
          var isOpen = postit.open;
          if (isOpen) {
            collapsed = collapsed.filter(function (item) { return item !== id; });
          } else {
            if (collapsed.indexOf(id) === -1) collapsed.push(id);
          }
          if (collapsed.length === 0) {
            localStorage.removeItem(storageKey);
          } else {
            localStorage.setItem(storageKey, JSON.stringify(collapsed));
          }
        } catch (e) {}
      });
    });
  }

  // Wire up the close (delete) button on each post-it. On click, sends DELETE
  // /comments/<id>. On success the SSE reload event will refresh the page.
  // On failure, logs to console without blocking the UI.
  function wireCloseButtons() {
    document.querySelectorAll(".postit-close").forEach(function (button) {
      button.addEventListener("click", function (event) {
        // Prevent the click from toggling the parent <details> element.
        event.stopPropagation();
        event.preventDefault();

        var postit = button.closest(".postit");
        if (!postit) return;
        var id = postit.dataset.commentId;

        fetch("/comments/" + id, { method: "DELETE" })
          .then(function (response) {
            if (!response.ok) {
              console.error(
                "Failed to delete comment " + id + ": HTTP " + response.status
              );
            }
            // On success the server writes the sidecar, which triggers an SSE
            // reload event. We do nothing here and let that event refresh the page.
          })
          .catch(function (err) {
            console.error("Network error deleting comment " + id + ":", err);
          });
      });
    });
  }

  // findNavTarget returns the post-it that a ▲ or ▼ click should scroll to,
  // or null if there is no target in that direction. The 1px epsilon prevents
  // sub-pixel rounding after a smooth scroll from leaving the just-landed
  // post-it as an immediate ▲ target.
  function findNavTarget(direction) {
    var referenceLine = window.innerHeight * 0.2;
    var postits = Array.from(document.querySelectorAll(".postit"));
    if (direction === "down") {
      return postits.find(function (postit) {
        return postit.getBoundingClientRect().top > referenceLine + 1;
      }) || null;
    }
    // direction === "up": last post-it whose top edge is above the line.
    for (var i = postits.length - 1; i >= 0; i--) {
      if (postits[i].getBoundingClientRect().top < referenceLine - 1) return postits[i];
    }
    return null;
  }

  // scrollPostitToReferenceLine scrolls so the given post-it's top edge lands
  // on the 20% reference line.
  function scrollPostitToReferenceLine(postit) {
    var referenceLine = window.innerHeight * 0.2;
    var rect = postit.getBoundingClientRect();
    window.scrollTo({
      top: rect.top + window.scrollY - referenceLine,
      behavior: "smooth",
    });
  }

  // updatePageNav refreshes the disabled state of the ▲/▼ buttons and toggles
  // .empty on the container when there are no post-its. Called at the end of
  // each layout pass (positionPostits, arrangePostits) and by the scroll and
  // resize listeners wired in wirePageNav.
  function updatePageNav() {
    var container = document.querySelector(".page-nav");
    if (!container) return;

    var postits = document.querySelectorAll(".postit");
    if (postits.length === 0) {
      container.classList.add("empty");
      return;
    }
    container.classList.remove("empty");

    var upButton = container.querySelector(".page-nav-up");
    var downButton = container.querySelector(".page-nav-down");
    if (upButton) {
      upButton.disabled = !findNavTarget("up");
    }
    if (downButton) {
      downButton.disabled = !findNavTarget("down");
    }
  }

  // wirePageNav attaches click handlers to the global ▲/▼ buttons and registers
  // the scroll and resize listeners that keep their disabled state current.
  function wirePageNav() {
    var container = document.querySelector(".page-nav");
    if (!container) return;

    var upButton = container.querySelector(".page-nav-up");
    var downButton = container.querySelector(".page-nav-down");

    if (downButton) {
      downButton.addEventListener("click", function () {
        var target = findNavTarget("down");
        if (target) scrollPostitToReferenceLine(target);
      });
    }

    if (upButton) {
      upButton.addEventListener("click", function () {
        var target = findNavTarget("up");
        if (target) scrollPostitToReferenceLine(target);
      });
    }

    // Keep disabled state in sync as the user scrolls or resizes. Passive
    // avoids blocking the scroll thread; resize uses the same handler since
    // both the reference line and post-it positions change.
    window.addEventListener("scroll", updatePageNav, { passive: true });
    window.addEventListener("resize", updatePageNav);
  }

  document.addEventListener("DOMContentLoaded", function () {
    // SSE-driven reloads replace the body, recreating the button. Re-sync its
    // glyph and aria-label here; data-theme on <html> already survived intact.
    var mode = readStoredMode();
    applyTheme(mode);
    syncToggleButton(mode);
    setAutoListener(mode === "auto");
    wireThemeToggle();
    arrangePostits();
    wireHover();
    wireToggleReposition();
    wireToggleCollapse();
    wireCloseButtons();
    wirePageNav();
    connectSSE();

    // Re-arrange when the breakpoint is crossed (e.g. device rotation,
    // window resize). Using matchMedia avoids re-running on every resize tick.
    window.matchMedia(MOBILE_QUERY).addEventListener("change", arrangePostits);
    window.matchMedia(WIDE_QUERY).addEventListener("change", positionPostits);
  });
})();
