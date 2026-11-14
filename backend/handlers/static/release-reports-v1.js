// The player owns source/profile state; reporting always binds to its prepared
// server selection and never submits a caller-provided source URL or filename.
let releaseReportState = null;
let releaseReportGeneration = 0;
let releaseReportReplacement = '';
let lastReleaseReportId = '';

function releaseReportLanguageName(code) {
  if (!code) return 'the requested language';
  try { return new Intl.DisplayNames([document.documentElement.lang || navigator.language || 'en'], { type: 'language' }).of(code) || code; }
  catch { return code; }
}

function updateReleaseReportButton() {
  const button = document.getElementById('releaseReportButton');
  if (button) button.hidden = SHARE_MODE || playbackMeta.isLive || !webPlayerActivePrequeueId;
}

async function openReleaseReport() {
  if (SHARE_MODE || playbackMeta.isLive || !webPlayerActivePrequeueId) return;
  const generation = ++releaseReportGeneration;
  const dialog = document.getElementById('releaseReportDialog');
  document.getElementById('releaseReportError').textContent = '';
  document.getElementById('releaseReportReason').value = '';
  document.getElementById('releaseReportUndo').hidden = true;
  dialog.showModal();
  setReleaseReportBusy(true);
  try {
    await webPlayerAudioPreferenceSave.catch(() => {});
    const id = webPlayerActivePrequeueId;
    const options = await apiCall('/playback/prequeue/' + encodeURIComponent(id) + '/report');
    if (generation !== releaseReportGeneration || !dialog.open) return;
    releaseReportState = { ...options, prequeueId: id, reportId: lastReleaseReportId };
    document.getElementById('releaseReportUndo').hidden = !lastReleaseReportId;
    document.getElementById('releaseReportName').textContent = options.releaseName || 'Selected release';
    const tracks = document.getElementById('releaseReportTrack');
    tracks.replaceChildren();
    (options.audioTracks || []).forEach(track => {
      const option = new Option(`${track.language ? releaseReportLanguageName(track.language) : 'Unknown language'} / ${track.codec || 'audio'} / track ${track.index}`, String(track.index));
      tracks.add(option);
    });
    if (options.selectedAudioTrack >= 0) tracks.value = String(options.selectedAudioTrack);
    document.querySelector('#releaseReportReason option[value="wrong_episode"]').disabled = !options.hasEpisode;
    document.querySelector('#releaseReportReason option[value="device_problem"]').disabled = !CLIENT_ID;
    updateReleaseReportScope();
    document.getElementById('releaseReportReason').focus();
  } catch (error) {
    releaseReportState = null;
    document.getElementById('releaseReportError').textContent = 'Could not load the current release. Close this dialog and try again.';
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
  document.getElementById('releaseReportTrackRow').hidden = reason !== 'audio_problem';
  document.getElementById('releaseReportScope').textContent = messages[reason] || 'A manual source change does not create a permanent report.';
  if (WATCH_PARTY_MODE) document.getElementById('releaseReportScope').textContent += ' Shared playback supports reporting only.';
  document.getElementById('releaseReportOnly').disabled = !state || !reason;
  document.getElementById('releaseReportRetry').disabled = !state || !reason || WATCH_PARTY_MODE;
  document.getElementById('releaseManualRetry').disabled = !state || WATCH_PARTY_MODE;
}

function setReleaseReportBusy(busy) {
  document.getElementById('releaseReportReason').disabled = busy;
  document.getElementById('releaseReportTrack').disabled = busy;
  if (busy) ['releaseReportOnly', 'releaseReportRetry', 'releaseManualRetry'].forEach(id => { document.getElementById(id).disabled = true; });
  else updateReleaseReportScope();
}

async function submitReleaseReport(retry) { return performReleaseAction(true, retry); }
async function retryReleaseWithoutReport() { return performReleaseAction(false, true); }

async function performReleaseAction(reporting, retry) {
  const state = releaseReportState;
  if (!state || (retry && WATCH_PARTY_MODE)) return;
  const reason = reporting ? document.getElementById('releaseReportReason').value : '';
  if (reporting && !reason) return;
  const generation = ++releaseReportGeneration;
  setReleaseReportBusy(true);
  document.getElementById('releaseReportError').textContent = '';
  try {
    const response = await apiCall('/playback/prequeue/' + encodeURIComponent(state.prequeueId) + (reporting ? '/report' : '/retry'), {
      method: 'POST',
      body: JSON.stringify({
        ...(reporting ? { reason, audioLanguage: state.audioLanguage || '' } : {}), sourceVersion: state.sourceVersion,
        ...(reason === 'audio_problem' ? { audioTrackIndex: Number(document.getElementById('releaseReportTrack').value) } : {}),
        retry, startOffset: currentAbsoluteTime(), skipHLS: true, clientId: CLIENT_ID,
      }),
    });
    if (response.report?.id) {
      lastReleaseReportId = response.report.id;
      state.reportId = response.report.id;
      document.getElementById('releaseReportUndo').hidden = false;
    }
    if (generation !== releaseReportGeneration) {
      if (response.replacement?.prequeueId) apiCall('/playback/prequeue/' + encodeURIComponent(response.replacement.prequeueId) + '/retry', { method: 'DELETE' }).catch(() => {});
      return;
    }
    if (!retry) {
      document.getElementById('releaseReportError').textContent = 'Report saved. Playback can continue; the exclusion applies to future choices.';
      return;
    }
    releaseReportReplacement = response.replacement?.prequeueId || '';
    if (!releaseReportReplacement) throw new Error('Report saved, but no alternative could be prepared.');
    document.getElementById('releaseReportError').textContent = 'Preparing another suitable source...';
    for (let attempt = 0; attempt < 80; attempt++) {
      if (generation !== releaseReportGeneration) return;
      const status = await apiCall('/playback/prequeue/' + encodeURIComponent(releaseReportReplacement));
      if (status.status === 'failed' || status.status === 'expired') throw new Error(reporting ? 'Report saved. No suitable alternative was found.' : 'No other suitable source was found.');
      if (status.status === 'ready' && status.streamPath) {
        const reset = reason === 'wrong_title' || reason === 'wrong_episode';
        const offset = reset ? 0 : Math.max(Number(response.resumePosition) || 0, currentAbsoluteTime());
        stopHlsSession({ skipProgress: reset });
        sourcePath = status.streamPath;
        directMovieUrl = '';
        webPlayerActivePrequeueId = status.prequeueId || releaseReportReplacement;
        webPlayerSelectedAudioTrack = Number(status.selectedAudioTrack) >= 0 ? Number(status.selectedAudioTrack) : null;
        webPlayerSelectedSubtitleTrack = Number(status.selectedSubtitleTrack ?? -1);
        webPlayerManualAudioSelection = webPlayerSelectedAudioTrack !== null;
        webPlayerManualSubtitleSelection = true;
        webPlayerMetadataResolved = false;
        playbackMeta = { ...playbackMeta, displayName: status.displayName || '', dv: Boolean(status.hasDolbyVision), hdr10: Boolean(status.hasHdr10), dvProfile: status.dolbyVisionProfile || '' };
        progressCompleted = false;
        lastKnownPositivePosition = reset ? 0 : offset;
        releaseReportReplacement = '';
        syncPlaybackUrlToHistory();
        renderHeader();
        document.getElementById('releaseReportDialog').close();
        await initializeWebMigrationState(webPlayerActivePrequeueId, status);
        await startWebPlayerFromSource({ startOffset: offset, skipStopProgress: reset, throwOnError: true });
        return;
      }
      await new Promise(resolve => setTimeout(resolve, 500));
    }
    throw new Error('The alternative took too long. Try another source manually.');
  } catch (error) {
    if (generation === releaseReportGeneration) document.getElementById('releaseReportError').textContent = error.message || 'Could not change the source.';
    if (releaseReportReplacement) {
      const id = releaseReportReplacement; releaseReportReplacement = '';
      apiCall('/playback/prequeue/' + encodeURIComponent(id) + '/retry', { method: 'DELETE' }).catch(() => {});
    }
  } finally { if (generation === releaseReportGeneration) setReleaseReportBusy(false); }
}

async function undoReleaseReport() {
  const state = releaseReportState;
  if (!state?.reportId) return;
  try {
    await apiCall('/playback/bad-streams/' + encodeURIComponent(state.reportId) + '?profileId=' + encodeURIComponent(activeProfileId), { method: 'DELETE' });
    state.reportId = '';
    lastReleaseReportId = '';
    document.getElementById('releaseReportUndo').hidden = true;
    document.getElementById('releaseReportError').textContent = 'Report removed.';
  } catch { document.getElementById('releaseReportError').textContent = 'Could not remove the report.'; }
}

function closeReleaseReport() {
  releaseReportGeneration++;
  if (releaseReportReplacement) {
    const id = releaseReportReplacement; releaseReportReplacement = '';
    apiCall('/playback/prequeue/' + encodeURIComponent(id) + '/retry', { method: 'DELETE', keepalive: true }).catch(() => {});
  }
  document.getElementById('releaseReportDialog').close();
  document.getElementById('releaseReportButton')?.focus();
}

document.getElementById('releaseReportDialog')?.addEventListener('cancel', event => { event.preventDefault(); closeReleaseReport(); });
document.addEventListener('DOMContentLoaded', updateReleaseReportButton);

window.addEventListener("pagehide", closeReleaseReport);
