// Shelf help is read-only: use the rendered shelf, including inherited settings.
(function (root) {
  const builtins = {
    "watch-together":
      "Invitations to shared Watch Together rooms and account invitations. Open a card to join or respond to an invitation.",
    "top-ten":
      "Ten titles from TMDB’s daily trending movies and TV shows. Movies are filtered to those released for home viewing. This is a discovery ranking, independent of viewing activity on this server.",
    "continue-watching":
      "Movies you have started and the next unwatched episodes of shows you are watching, based on this profile’s playback progress and watch history.",
    "watch-something":
      "Shortcuts to search for a title, ask AI for suggestions, or get a surprise pick. AI actions require a configured AI provider.",
    tonight:
      "A selection drawn from Continue Watching, recommendations, recently aired titles, and your watchlist to help choose something for the evening.",
    "my-recommended":
      "Personalized movie and TV suggestions based on this profile’s recent watch history and playback progress, with trending titles as a fallback. Titles already in your history or progress are excluded. No AI provider is required.",
    "my-upcoming":
      "Shows you are watching whose next unwatched episode has not aired yet. These come from Continue Watching, rather than your watchlist or the wider release calendar. Use Configure sort to choose their order.",
    calendar:
      "A release calendar of movies and TV episodes from the sources enabled in Calendar settings: watchlist, Continue Watching, trending titles, and MDBList shelves. Home includes recent releases and upcoming releases; the heading changes as you browse them.",
    "my-recently-aired":
      "Movies and TV episodes that have already released or aired within the Home calendar’s recent window. This uses the shelf’s selected calendar sources and defaults to your watchlist. It describes release activity, rather than what anyone has watched.",
    watchlist:
      "Movies and shows saved to this profile’s watchlist, including items synced from connected watchlist services.",
    "trending-movies":
      "Movies from the backend’s MDBList trending movie feed. This is a discovery list, independent of this profile’s watch history and the server’s viewing activity.",
    "trending-tv":
      "Shows from the backend’s MDBList trending TV feed. This is a discovery list, independent of this profile’s watch history and the server’s viewing activity.",
    "streaming-services":
      "Cards for configured streaming services. Open a service to browse its configured movie and TV lists; the cards represent collections, rather than individual titles.",
    "live-favorites":
      "Live TV channels marked as favorites for this profile. Open a channel to watch it; available channels depend on the configured live TV sources.",
    recordings:
      "Live TV recordings for this profile that can be played, the one recording now first and then newest first. Open a recording to play it. Requires Live TV with DVR recording.",
    "popular-on-server":
      "Movies and shows watched by multiple profiles on this server, ranked by shared viewing activity. Only profiles that opt in to sharing activity contribute. Configure the time window and minimum number of profiles.",
    "recently-watched":
      "Movies and shows recently watched by profiles on this server that opt in to sharing activity. Configure the time window and per-profile cap. These are watched titles, rather than newly released titles.",
    dashboard:
      "Current playback and streaming activity on this server, including active sessions and stream details.",
    "permanent-prequeue":
      "Titles with permanent prequeued streams saved for this profile. Temporary prequeue entries are excluded.",
    "set-aside":
      "Titles you have set aside from Continue Watching. Keep them here to return to later without keeping them in your active Continue Watching shelf.",
  };
  builtins["gemini-recs"] = builtins["my-recommended"];

  const types = {
    mdblist:
      "Titles from the configured MDBList list, in the list’s supplied order.",
    stremio: "Titles from the selected catalog of a configured Stremio add-on.",
    tmdb: "Titles from the selected TMDB list, collection, or discovery query, using the configured filters and sort order.",
    trakt:
      "Titles from the selected Trakt account’s watchlist, collection, recommendations, or custom list.",
    publicmetadb: "Titles from the selected PublicMetaDB account and list.",
    simkl:
      "Titles from the selected Simkl account and movie, TV, or anime list.",
    letterboxd:
      "Movies from the configured public Letterboxd list or watchlist, or its selected MDBList import.",
    genre:
      "Movies or shows matching the selected genre, discovered through TMDB.",
    decade:
      "Movies or shows from the selected release decade, discovered through TMDB.",
    library: "Titles from the selected local, Plex, or Jellyfin media library.",
    "local-library":
      "Titles from the selected local, Plex, or Jellyfin media library.",
    "collection-hub":
      "Cards linking to the shelves included in this collection hub. Open a card to browse that shelf’s titles.",
  };

  function getDescription(shelf) {
    // IDs are stable even when a shelf is renamed or an old config omits type.
    let description = shelf.listUrl?.startsWith("mediastorm:custom-list:")
      ? "Titles saved to the selected person’s custom list, with the most recently added titles first."
      : builtins[shelf.id] || types[shelf.type];
    if (!description && shelf.listUrl) description = types.mdblist;
    if (!description)
      description =
        "Titles from this shelf’s configured source. Its contents depend on the source and the filters selected for this shelf.";
    if (shelf.id === "my-recently-aired") {
      const sources = shelf.calendarSources || {};
      const names = [];
      if (sources.watchlist !== false) names.push("Watchlist");
      if (sources.history === true) names.push("Continue Watching");
      if (sources.topTrending === true) names.push("Top Trending");
      if (sources.trending === true) names.push("Trending");
      if (sources.mdblists === true) names.push("MDBList shelves");
      description += " Selected sources: " + (names.join(", ") || "none") + ".";
    }
    if (!builtins[shelf.id] && shelf.hideUnreleased) {
      description +=
        " Hide Unreleased is enabled, so movies not yet available for home viewing and upcoming shows are filtered out.";
    }
    return description;
  }

  function escapeAttribute(value) {
    return String(value).replace(
      /[&<>"']/g,
      (character) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[character],
    );
  }

  function renderButton(shelf) {
    const name = shelf.name || shelf.id;
    return (
      '<button type="button" class="shelf-info-btn" aria-haspopup="dialog" aria-label="About ' +
      escapeAttribute(name) +
      '" title="About this shelf" data-shelf-name="' +
      escapeAttribute(name) +
      '" data-shelf-info="' +
      escapeAttribute(getDescription(shelf)) +
      '" onclick="ShelfInfo.open(this)"><svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 11v6"/><circle cx="12" cy="7" r="0.5" fill="currentColor"/></svg></button>'
    );
  }

  function open(button) {
    const existing = document.getElementById("shelfInfoDialog");
    if (existing) existing.close();
    const dialog = document.createElement("dialog");
    dialog.id = "shelfInfoDialog";
    dialog.className = "shelf-info-dialog";
    dialog.setAttribute("aria-labelledby", "shelfInfoTitle");
    dialog.setAttribute("aria-describedby", "shelfInfoDescription");
    dialog.innerHTML =
      '<div class="shelf-info-header"><h3 id="shelfInfoTitle"></h3><button type="button" class="shelf-info-close" aria-label="Close shelf information" autofocus>&times;</button></div><p id="shelfInfoDescription"></p>';
    dialog.querySelector("h3").textContent = button.dataset.shelfName;
    dialog.querySelector("p").textContent = button.dataset.shelfInfo;
    dialog
      .querySelector("button")
      .addEventListener("click", () => dialog.close());
    dialog.addEventListener("click", (event) => {
      const rect = dialog.getBoundingClientRect();
      if (
        event.target === dialog &&
        (event.clientX < rect.left ||
          event.clientX > rect.right ||
          event.clientY < rect.top ||
          event.clientY > rect.bottom)
      )
        dialog.close();
    });
    const previousOverflow = document.body.style.overflow;
    dialog.addEventListener(
      "close",
      () => {
        document.body.style.overflow = previousOverflow;
        dialog.remove();
        if (button.isConnected) button.focus();
      },
      { once: true },
    );
    document.body.appendChild(dialog);
    document.body.style.overflow = "hidden";
    dialog.showModal();
  }

  const api = { getDescription, renderButton, open };
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.ShelfInfo = api;
})(typeof window !== "undefined" ? window : globalThis);
