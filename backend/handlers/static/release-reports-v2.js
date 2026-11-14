// Reports and replacements bind to the server-selected source and the current
// profile. A replacement remains inactive until the viewer confirms the handoff.
let releaseReportState = null;
let releaseReportAction = null;
let releaseReportGeneration = 0;
let releaseReportBusy = false;
let lastReleaseReportId = '';
let lastReleaseReportProfile = '';
let lastReleaseReportGeneration = 0;
const releaseReportRequestTimeoutMs = 15000;

function releaseReportLanguageName(code) {
  if (!code) return 'the requested language';
  try { return new Intl.DisplayNames([document.documentElement.lang || navigator.language || 'en'], { type: 'language' }).of(code) || code; }
  catch { return code; }
}

function updateReleaseReportButton() {
  const button = document.getElementById('releaseReportButton');
  if (button) button.hidden = SHARE_MODE || playbackMeta.isLive || !webPlayerActivePrequeueId;
}

function releaseReportBlocksMigration() { return Boolean(document.getElementById('releaseReportDialog')?.open); }

function releaseReportContextMatches(state) {
  return Boolean(state && state.profileId === activeProfileId && state.prequeueId === webPlayerActivePrequeueId &&
    state.sourcePath === sourcePath && state.audioGeneration === webPlayerAudioPreferenceGeneration);
}

function releaseActionCurrent(action) {
  return Boolean(action && !action.canceled && action === releaseReportAction && action.generation === releaseReportGeneration &&
    action.state === releaseReportState && releaseReportContextMatches(action.state) && document.getElementById('releaseReportDialog').open);
}

// Bound the UI wait. Keep observing a late POST response so its preparation can
// still be canceled after the viewer has closed the dialog or the wait timed out.
async function releaseReportWait(promise) {
  let timer;
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error('The request took too long. Close this dialog and try again.')), releaseReportRequestTimeoutMs);
    })]);
  } finally { clearTimeout(timer); }
}

function cancelReleaseAction(action = releaseReportAction, keepalive = false) {
  if (!action) return;
  action.canceled = true;
  clearInterval(action.monitor);
  if (releaseReportAction === action) releaseReportAction = null;
  if (!action.transferred && action.replacementId && !action.cleanupSent && action.state.profileId === activeProfileId) {
    const current = releaseReportAction;
    // The server deduplicates preparations. A late response can name the exact
    // entry a newer request now owns, or the source already playing.
    if (action.replacementId === webPlayerActivePrequeueId) action.cleanupSent = true;
    else if (current && current !== action && current.generation > action.generation && current.state.profileId === action.state.profileId && !current.canceled) {
      if (!current.responseReceived) {
        current.deferredCleanup.add(action);
      } else if (current.replacementId === action.replacementId) action.cleanupSent = true;
      else sendReleaseCleanup(action, keepalive);
    } else sendReleaseCleanup(action, keepalive);
  }
  for (const previous of action.deferredCleanup || []) cancelReleaseAction(previous, keepalive);
  action.deferredCleanup?.clear();
}

function sendReleaseCleanup(action, keepalive) {
  action.cleanupSent = true;
  apiCall('/playback/prequeue/' + encodeURIComponent(action.replacementId) + '/retry', { method: 'DELETE', keepalive }).catch(() => {});
}

function refreshReleaseUndo() {
  const available = Boolean(lastReleaseReportId && lastReleaseReportProfile === activeProfileId);
  document.getElementById('releaseReportUndo').hidden = !available;
  document.getElementById('releaseReportUndo').disabled = releaseReportBusy;
}

function rememberReleaseReport(action, response) {
  if (!response?.report?.id || action.state.profileId !== activeProfileId || action.generation < lastReleaseReportGeneration) return;
  lastReleaseReportId = response.report.id;
  lastReleaseReportProfile = action.state.profileId;
  lastReleaseReportGeneration = action.generation;
  refreshReleaseUndo();
}

async function openReleaseReport() {
  if (SHARE_MODE || playbackMeta.isLive || !webPlayerActivePrequeueId) return;
  if (webPlayerMigrationInProgress || webPlayerMigrationProbation) {
    setStatus('A source change is already in progress. Report after it finishes.');
    return;
  }
  cancelReleaseAction();
  const generation = ++releaseReportGeneration;
  releaseReportState = null;
  const dialog = document.getElementById('releaseReportDialog');
  document.getElementById('releaseReportError').textContent = '';
  document.getElementById('releaseReportReason').value = '';
  document.getElementById('releaseReplacementReady').hidden = true;
  document.getElementById('releaseReplacementResume').value = 'rewind';
  dialog.showModal();
  setReleaseReportBusy(true);
  try {
    await releaseReportWait(webPlayerAudioPreferenceSave);
    if (webPlayerAudioPreferenceSaveFailed) {
      const error = new Error('Audio language could not be saved. Select it again before reporting or preparing an alternative.');
      error.releaseMessage = true;
      throw error;
    }
    const binding = { prequeueId: webPlayerActivePrequeueId, profileId: activeProfileId, sourcePath, audioGeneration: webPlayerAudioPreferenceGeneration };
    const options = await releaseReportWait(apiCall('/playback/prequeue/' + encodeURIComponent(binding.prequeueId) + '/report'));
    if (generation !== releaseReportGeneration || !dialog.open) return;
    if (!releaseReportContextMatches(binding)) throw new Error('Playback changed. Reopen the dialog.');
    releaseReportState = { ...options, ...binding };
    document.getElementById('releaseReportName').textContent = options.releaseName || 'Selected release';
    const tracks = document.getElementById('releaseReportTrack');
    tracks.replaceChildren();
    (options.audioTracks || []).forEach(track => tracks.add(new Option(`${track.language ? releaseReportLanguageName(track.language) : 'Unknown language'} / ${track.codec || 'audio'} / track ${track.index}`, String(track.index))));
    if (options.selectedAudioTrack >= 0) tracks.value = String(options.selectedAudioTrack);
    document.querySelector('#releaseReportReason option[value="wrong_episode"]').disabled = !options.hasEpisode;
    document.querySelector('#releaseReportReason option[value="device_problem"]').disabled = !CLIENT_ID;
    document.getElementById('releaseReportReason').focus();
  } catch (error) {
    if (generation === releaseReportGeneration) {
      releaseReportState = null;
      document.getElementById('releaseReportError').textContent = error.releaseMessage ? error.message : 'Could not load the current selection. Close this dialog and try again.';
    }
  } finally { if (generation === releaseReportGeneration) setReleaseReportBusy(false); }
}

function updateReleaseReportScope() {
  const state = releaseReportState;
  const reason = document.getElementById('releaseReportReason').value;
  const owner = state?.canShare ? 'All profiles on this server' : 'This profile';
  const language = releaseReportLanguageName(state?.audioLanguage);
  const messages = {
    wrong_title: `${owner}: exclude this release for the requested title or episode. Other titles and package episodes remain eligible.`,
    wrong_episode: `${owner}: exclude this release for this episode. The replacement starts from the beginning.`,
    missing_language: `${owner}: avoid this release for ${language} audio requests. Other languages remain eligible.`,
    audio_problem: `${owner}: skip the affected audio track. Healthy alternate tracks remain eligible.`,
    file_problem: `${owner}: exclude this file for this title or episode.`,
    quality_problem: 'Avoid this release for this profile. Other profiles remain unaffected.',
    device_problem: 'Avoid this release for this profile on this device. Other devices remain eligible.',
    source_unavailable: 'Avoid this source route for this profile for 15 minutes. Other working providers remain eligible.',
  };
  const locked = releaseReportBusy || Boolean(releaseReportAction);
  document.getElementById('releaseReportTrackRow').hidden = reason !== 'audio_problem';
  document.getElementById('releaseReportScope').textContent = (releaseReportAction && !releaseReportAction.reporting ?
    'Manual source change: this preparation creates no permanent report.' : (messages[reason] || 'A manual source change does not create a permanent report.')) +
    (WATCH_PARTY_MODE ? ' Shared playback supports reporting only.' : '');
  document.getElementById('releaseReportReason').disabled = locked;
  document.getElementById('releaseReportTrack').disabled = locked;
  ['releaseReportOnly', 'releaseReportRetry', 'releaseManualRetry'].forEach(id => { document.getElementById(id).hidden = Boolean(releaseReportAction?.ready); });
  document.getElementById('releaseReportOnly').disabled = locked || !state || !reason;
  document.getElementById('releaseReportRetry').disabled = locked || !state || !reason || WATCH_PARTY_MODE;
  document.getElementById('releaseManualRetry').disabled = locked || !state || WATCH_PARTY_MODE;
  document.getElementById('releaseReplacementStart').disabled = releaseReportBusy || !releaseReportAction?.ready;
  refreshReleaseUndo();
}

function setReleaseReportBusy(busy) { releaseReportBusy = busy; updateReleaseReportScope(); }
async function submitReleaseReport(retry) { return performReleaseAction(true, retry); }
async function retryReleaseWithoutReport() { return performReleaseAction(false, true); }

async function performReleaseAction(reporting, retry) {
  const state = releaseReportState;
  if (releaseReportBusy || releaseReportAction || !state || (retry && WATCH_PARTY_MODE)) return;
  if (!releaseReportContextMatches(state)) { invalidateReleaseSelection(); return; }
  const reason = document.getElementById('releaseReportReason').value;
  if (reporting && !reason) return;
  const action = { state, reason, reporting, generation: ++releaseReportGeneration, replacementId: '', canceled: false, ready: null, transferred: false, responseReceived: false, deferredCleanup: new Set() };
  releaseReportAction = action;
  setReleaseReportBusy(true);
  document.getElementById('releaseReportError').textContent = '';
  action.monitor = setInterval(() => {
    if (!action.transferred && action === releaseReportAction && !releaseReportContextMatches(state)) invalidateReleaseSelection();
  }, 250);
  const request = apiCall('/playback/prequeue/' + encodeURIComponent(state.prequeueId) + (reporting ? '/report' : '/retry'), {
    method: 'POST',
    body: JSON.stringify({
      ...(reporting ? { reason, audioLanguage: state.audioLanguage || '' } : {}), sourceVersion: state.sourceVersion,
      ...(reporting && reason === 'audio_problem' ? { audioTrackIndex: Number(document.getElementById('releaseReportTrack').value) } : {}),
      retry, startOffset: 0, skipHLS: true, clientId: CLIENT_ID,
    }),
  });
  // Observe this exact action, including responses received after cancellation.
  request.then(response => {
    rememberReleaseReport(action, response);
    action.replacementId = response?.replacement?.prequeueId || '';
    action.responseReceived = true;
    for (const previous of action.deferredCleanup) cancelReleaseAction(previous);
    action.deferredCleanup.clear();
    if (!releaseActionCurrent(action)) cancelReleaseAction(action);
  }).catch(() => {});
  try {
    const response = await releaseReportWait(request);
    if (!releaseActionCurrent(action)) { cancelReleaseAction(action); return; }
    if (!retry) {
      cancelReleaseAction(action);
      document.getElementById('releaseReportError').textContent = 'Report saved. Playback can continue; the exclusion applies to future choices.';
      return;
    }
    if (!action.replacementId) throw new Error('No alternative preparation was returned.');
    document.getElementById('releaseReportError').textContent = 'Preparing another suitable source. Your current source remains open.';
    const deadline = Date.now() + 60000;
    while (Date.now() < deadline) {
      const status = await releaseReportWait(apiCall('/playback/prequeue/' + encodeURIComponent(action.replacementId)));
      if (!releaseActionCurrent(action)) { cancelReleaseAction(action); return; }
      if (status.status === 'failed' || status.status === 'expired') throw new Error('No suitable alternative was found. Your current source is unchanged.');
      if (status.status === 'ready' && status.streamPath) {
        action.ready = status;
        renderPreparedRelease(action);
        document.getElementById('releaseReportError').textContent = 'Alternative ready. Review the source and start position, then confirm the switch.';
        return;
      }
      await new Promise(resolve => setTimeout(resolve, 500));
    }
    throw new Error('The alternative took too long. Try another source manually.');
  } catch {
    if (releaseActionCurrent(action)) document.getElementById('releaseReportError').textContent = 'Could not prepare a suitable alternative. Your current source is unchanged. Any saved report remains saved.';
    cancelReleaseAction(action);
  } finally { if (action.generation === releaseReportGeneration) setReleaseReportBusy(false); }
}

function renderPreparedRelease(action) {
  const status = action.ready;
  const reset = action.reason === 'wrong_title' || action.reason === 'wrong_episode';
  document.getElementById('releaseReplacementReady').hidden = false;
  document.getElementById('releaseReplacementName').textContent = status.displayName || status.selectedResult?.title || 'Selected alternative';
  const audio = (status.audioTracks || []).find(track => track.index === status.selectedAudioTrack);
  document.getElementById('releaseReplacementAudio').textContent = audio ? `Selected audio: ${releaseReportLanguageName(audio.language)} / ${audio.codec || 'audio'}` : 'Audio selection follows this playback request.';
  const resume = document.getElementById('releaseReplacementResume');
  if (reset) resume.value = 'beginning';
  resume.disabled = reset;
  document.getElementById('releaseReplacementTiming').textContent = reset ? 'Wrong movie or episode: the replacement starts from the beginning.' :
    'Position is calculated when you switch. Different edits may use different timing.';
}

function preparedReleaseOffset(mode, position, wrongContent, duration) {
  if (wrongContent || mode === 'beginning') return 0;
  let offset = Math.max(0, Number(position) || 0);
  if (mode === 'rewind') offset = Math.max(0, offset - 10);
  return Number(duration) > 0 && offset >= Number(duration) ? 0 : offset;
}

async function startPreparedRelease() {
  const action = releaseReportAction;
  if (releaseReportBusy || !action?.ready) return;
  if (!releaseActionCurrent(action)) { invalidateReleaseSelection(); return; }
  setReleaseReportBusy(true);
  try {
    const status = await releaseReportWait(apiCall('/playback/prequeue/' + encodeURIComponent(action.replacementId)));
    if (!releaseActionCurrent(action)) { cancelReleaseAction(action); return; }
    if (status.status !== 'ready' || !status.streamPath) throw new Error('The prepared alternative is no longer ready.');
    if (status.streamPath !== action.ready.streamPath || status.selectedAudioTrack !== action.ready.selectedAudioTrack) {
      action.ready = status;
      renderPreparedRelease(action);
      document.getElementById('releaseReportError').textContent = 'The prepared source changed. Review the updated alternative before switching.';
      return;
    }
    const reset = action.reason === 'wrong_title' || action.reason === 'wrong_episode';
    const offset = preparedReleaseOffset(document.getElementById('releaseReplacementResume').value, currentAbsoluteTime(), reset, status.duration);
    action.transferred = true;
    clearInterval(action.monitor);
    stopHlsSession({ skipProgress: reset });
    sourcePath = status.streamPath;
    directMovieUrl = '';
    webPlayerActivePrequeueId = status.prequeueId || action.replacementId;
    webPlayerSelectedAudioTrack = Number(status.selectedAudioTrack) >= 0 ? Number(status.selectedAudioTrack) : null;
    webPlayerSelectedSubtitleTrack = Number(status.selectedSubtitleTrack ?? -1);
    webPlayerManualAudioSelection = webPlayerSelectedAudioTrack !== null;
    webPlayerManualSubtitleSelection = true;
    webPlayerMetadataResolved = false;
    playbackMeta = { ...playbackMeta, displayName: status.displayName || '', dv: Boolean(status.hasDolbyVision), hdr10: Boolean(status.hasHdr10), dvProfile: status.dolbyVisionProfile || '' };
    progressCompleted = false;
    lastKnownPositivePosition = offset;
    syncPlaybackUrlToHistory();
    renderHeader();
    await initializeWebMigrationState(webPlayerActivePrequeueId, status);
    await startWebPlayerFromSource({ startOffset: offset, skipStopProgress: reset, throwOnError: true });
    if (releaseReportAction === action) {
      releaseReportAction = null;
      releaseReportState = null;
      setReleaseReportBusy(false);
      releaseReportGeneration++;
      document.getElementById('releaseReportDialog').close();
    }
  } catch {
    if (action === releaseReportAction) {
      document.getElementById('releaseReportError').textContent = action.transferred ? 'The alternative could not start. Close this dialog to use the player controls. Your report can still be undone.' :
        'The prepared alternative could not be verified. Your current source is unchanged.';
      document.getElementById('releaseReplacementReady').hidden = true;
      if (action.transferred) releaseReportState = null;
      cancelReleaseAction(action);
    }
  } finally { if (action.generation === releaseReportGeneration) setReleaseReportBusy(false); }
}

function invalidateReleaseSelection() {
  cancelReleaseAction();
  releaseReportGeneration++;
  releaseReportState = null;
  document.getElementById('releaseReplacementReady').hidden = true;
  document.getElementById('releaseReportError').textContent = 'Playback, profile or audio intent changed. Close this dialog and reopen it for the current selection.';
  setReleaseReportBusy(false);
}

async function undoReleaseReport() {
  if (releaseReportBusy || !lastReleaseReportId || lastReleaseReportProfile !== activeProfileId) return;
  const id = lastReleaseReportId;
  const profile = activeProfileId;
  cancelReleaseAction();
  const generation = ++releaseReportGeneration;
  document.getElementById('releaseReplacementReady').hidden = true;
  setReleaseReportBusy(true);
  try {
    await releaseReportWait(apiCall('/playback/bad-streams/' + encodeURIComponent(id) + '?profileId=' + encodeURIComponent(profile), { method: 'DELETE' }));
    if (id === lastReleaseReportId && profile === lastReleaseReportProfile) lastReleaseReportId = '';
    if (profile === activeProfileId && generation === releaseReportGeneration) document.getElementById('releaseReportError').textContent = 'Report removed.';
  } catch { if (profile === activeProfileId && generation === releaseReportGeneration) document.getElementById('releaseReportError').textContent = 'Could not remove the report.'; }
  finally { if (generation === releaseReportGeneration) setReleaseReportBusy(false); else refreshReleaseUndo(); }
}

function closeReleaseReport() {
  cancelReleaseAction(releaseReportAction, true);
  releaseReportGeneration++;
  releaseReportState = null;
  setReleaseReportBusy(false);
  document.getElementById('releaseReportDialog').close();
  document.getElementById('releaseReportButton')?.focus();
}

document.getElementById('releaseReportDialog')?.addEventListener('cancel', event => { event.preventDefault(); closeReleaseReport(); });
document.addEventListener('DOMContentLoaded', updateReleaseReportButton);
window.addEventListener('pagehide', closeReleaseReport);
