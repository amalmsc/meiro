# Lyrics

The Now Playing panel has a Lyrics tab, with the lyrics of the song playing. A
song with timed lyrics lights the line being sung and keeps it in the middle;
songs whose source times each word light each word as it is sung. The mini
player and the macOS notch show the same lyrics in their own layouts.

## Sub-features

- `lyrics-order`: Better Lyrics is asked first, then YouTube Music, then LRCLIB, and the first source with timed lyrics wins. A source that fails or has nothing is skipped quietly.
- `lyrics-credits`: the lookup names the track's artist, album and length, which Better Lyrics keys its cache on. An artist a list left out is filled in from what another list named, and a lookup made before the track's length was known is made again once it is.
- `lyrics-word-sync`: Better Lyrics times each word, or syllable for a song in Japanese or Korean, so a line fills in as the singer reaches each word. Words still to come are dim.
- `lyrics-line-sync`: YouTube Music and LRCLIB time whole lines, which stay lit for as long as they are sung and fade with their distance from it.
- `lyrics-source`: the question mark at the end of the row opens a dialog that names where the lyrics came from (Better Lyrics, YouTube Music or LRCLIB) and the order Meiro asks them in. The button is absent when a source named none.
- `lyrics-modes`: a switch under the tabs turns the timed lines on and off, remembered in Settings; off shows the plain text. The clock beside it is what the switch turns on, and the label "Synced lyrics" is the switch's own. Static shows the words of the timed lines joined, when the source has no plain text.
- `lyrics-follow`: the line being sung sits in the middle of the list, in the theme's accent colour, and every other line is dimmer and smaller the further it is from it. The list dissolves into the card at its top and bottom instead of being cut by them, and reads at a fixed measure rather than the width of the tab. Opening the tab during a song places the current line in the middle at once.
- `lyrics-seek`: clicking a line seeks to it.
- `lyrics-fallback`: no lyrics and a failed lookup are different states; a failure is tried again the next time the tab opens.
- `lyrics-cache`: lyrics are kept per track while it plays; changing songs supersedes the lookup.
- `lyrics-floating`: Open floating lyrics in the Lyrics tab opens one movable, resizable, always-on-top window. It shares the lookup and playback position, but follows and scrolls independently. It keeps loading songs with both player panels closed, supports static lyrics and seeking, and can be closed and reopened. Native clear glass reveals other windows on macOS (Liquid Glass on 26+, HUD material earlier) and Acrylic on Windows 11; systems without a material keep a solid background. Lyrics stay opaque and the synced list must not paint opaque fade strips over glass.

## How to get to it (user POV)

Play a song. Open the mini player from the tray (Click the music note in the
menu bar on macOS, or the tray icon on Linux and Windows), or open Now Playing
in the main window on a wide screen, and choose Lyrics. With word-synced
lyrics, the line being sung fills in word by word. The name at the right of the
Synced and Static buttons is where the lyrics came from.

Click **Floating lyrics** at the bottom of the navigation rail on any page,
or **Open floating lyrics** (the pop-out icon) in either player's Lyrics tab.
Drag its native title bar to move it and its edges to resize it. Close
it with its close button; reopening an already open window focuses it instead
of opening another copy. Hiding the main window leaves floating lyrics visible.
Previous, play/pause, next, shuffle and repeat stay visible. **Show player
controls** expands artwork, track details, seeking, volume and the playback
speed menu; **Hide player controls** collapses them without hiding the lyrics.
Its Up next, Lyrics and Related tabs are independent of the main/tray player's
selected tab. Episodes show Details instead of Lyrics. Queue actions change
shared playback; related songs play and related collections open in the main
window without closing the floating player.

## Driving it with the headless tester

Set `a.betterLyrics` to return `[]lyrics.Line` with `Words` for word-synced
lyrics, or `a.lyricsSource` for LRCLIB lines. Both are stubbed to nothing in
`newTestApp(t)`, so no test reaches the network. Set `a.total` together with
`a.resumeID` and `a.scrub` (in seconds) to place playback at a moment: without
a known duration the player's scrubber resets `a.scrub` to 0 every frame.

The mini player shows the lyrics tab at 344 × 640. The Now Playing panel only
shows its side panel on a wide window: build `ui.NewTester(a.view, 1400, 800)`,
call `a.openPlayer(tabLyrics, false)`, draw a frame, then advance the clock so
the player finishes rising.

After clicking Open floating lyrics, build a second tester with
`a.floatingLyricsView` at 480 × 480 and also check its minimum 320 × 320 size
(288 pixels of content under the macOS native title bar).
Alternate frames in both windows and verify each centers the current line.
Switch to Up next and close both main/tray panels before changing songs to
check the floating window alone keeps loading lyrics. Native checks must
cover always-on-top, moving/resizing, reuse, closing/reopening, and keeping
the panel visible while the main window hides. An offline macOS fixture
verified these native lifecycle checks; Windows and Linux remain unverified.

Expand and collapse the controls at both 480 and 320 pixels wide. Expansion
raises the native minimum height to 480 so lyrics still fit; collapse permits
320 again. Verify the current lyric stays visible after resizing and toggling.
Drag the floating position slider, build a main-window frame while held, then
release: only release should seek. Also exercise mute and Next with both other
panels closed. The offline macOS fixture verified both control layouts over
glass, including the narrow expanded and minimum collapsed sizes.
Click the rail button on Home, Explore, Search, Settings, Stats and a detail
page, with the rail both collapsed and expanded: it must open without navigating.
Exercise Up next, Related and Lyrics while the main player's tab stays unchanged,
including Clear queue and playing a related song. The offline macOS fixture
visually checked every tab at normal/minimum size and expanded lyrics/related.

For glass, run the tester with `SetVibrancy(true)` and verify alpha is zero in
blank regions, including the top/bottom of the synced list, while the current
lyric still has opaque pixels. Toggle back to false to verify the solid fallback.
Native verification must put a colored offline fixture behind the panel and
capture the actual screen region: a single-window screenshot omits the backdrop
and makes clear glass look black. macOS synced/static glass and the solid
fallback were visually checked this way; Windows Acrylic remains unverified.

## Gotchas

- The "Synced lyrics" label of the mode switch and the "Where these lyrics come
  from" label of the source button repeat on both the panel and the mini player;
  a tester can only click what its own window shows.
- A line with no words still renders through `m3.EmphasizedText`; only lines
  with `Words` use `ui.RichText`, so a lyric-only line keeps its old look.
- The source dialog drops the `Source: ` prefix of the footer before showing it,
  so assert on `Better Lyrics`, not `Source: Better Lyrics`.
- Better Lyrics serves cached songs without a key and wants one for the rest,
  so a live run can show YouTube Music or LRCLIB lyrics for a song it has not
  cached. That is the fallback working, not a failure.
- Better Lyrics finds a cached song by artist and length, so `a.total` matters
  in tests too: a source stub can assert the duration it was asked for.
- The timed view places itself from the boxes it measures, so a proof that
  opens the tab mid-song must draw a few frames before it reads positions: the
  first frame places the line, the next one corrects it for the card still
  rising. `TestTheSungLineSitsInTheMiddleOfTheLyrics` is the committed check.
- Album rows from YouTube Music carry no artist, so album playback asks Better
  Lyrics with none and falls through unless another list named the artist. A
  playlist row carries both, and is the way to see Better Lyrics live.
