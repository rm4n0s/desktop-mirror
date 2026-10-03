'use strict';

const $ = (id) => document.getElementById(id);
const intro = $('intro');
const stage = $('stage');
const message = $('message');
const startBtn = $('start');

let localStream = null;
let facing = 'user';
let ws = null;
let pc = null;
let queue = Promise.resolve();

function say(text) { message.textContent = text; }

function showStage(on) {
  stage.hidden = !on;
  intro.hidden = on;
}

async function openCamera() {
  return navigator.mediaDevices.getUserMedia({
    audio: false,
    video: { facingMode: facing, width: { ideal: 1280 }, height: { ideal: 720 }, frameRate: { ideal: 30 } },
  });
}

async function start() {
  startBtn.disabled = true;
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    say('This browser cannot access the camera on this page. Open the link over HTTPS in a current browser.');
    startBtn.disabled = false;
    return;
  }
  try {
    localStream = await openCamera();
  } catch (err) {
    say('Camera access was denied or is unavailable: ' + err.name);
    startBtn.disabled = false;
    return;
  }
  $('local').srcObject = localStream;
  say('Connecting to your computer…');
  connect();
}

function connect() {
  const url = new URL('ws', location.href);
  url.protocol = 'wss:';
  ws = new WebSocket(url);
  ws.onopen = () => send({ type: 'hello', ua: navigator.userAgent });
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    // Handle messages strictly in order: setRemoteDescription is async and
    // candidates must not overtake the offer.
    queue = queue.then(() => handle(msg)).catch((err) => fail(err));
  };
  ws.onclose = () => end('Disconnected. Scan the QR code on your computer again to reconnect.');
  ws.onerror = () => say('Could not reach your computer. Are you on the same Wi-Fi?');
}

function send(msg) {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
}

async function handle(msg) {
  switch (msg.type) {
    case 'offer': return onOffer(msg);
    case 'candidate': return pc && msg.candidate ? pc.addIceCandidate(msg) : undefined;
    case 'error': return end(msg.code === 'session_busy'
      ? 'This computer is already paired with a phone. Use "New QR code" on the computer.'
      : 'The pairing code expired. Use "New QR code" on the computer.');
    case 'bye': return end('The computer ended the session.');
  }
}

async function onOffer(msg) {
  pc = new RTCPeerConnection({ iceServers: [] });
  pc.onicecandidate = (ev) => send(ev.candidate ? { type: 'candidate', ...ev.candidate.toJSON() } : { type: 'candidate' });
  pc.ontrack = (ev) => {
    $('remote').srcObject = ev.streams[0] || new MediaStream([ev.track]);
    showStage(true);
  };
  pc.onconnectionstatechange = () => {
    if (pc && (pc.connectionState === 'failed' || pc.connectionState === 'closed')) {
      end('Connection lost. Scan the QR code on your computer again.');
    }
  };

  await pc.setRemoteDescription({ type: 'offer', sdp: msg.sdp });
  // The offer carries one video m-line; attach the phone camera to it.
  const transceiver = pc.getTransceivers().find((t) => t.receiver.track.kind === 'video');
  await transceiver.sender.replaceTrack(localStream.getVideoTracks()[0]);
  transceiver.direction = 'sendrecv';
  const answer = await pc.createAnswer();
  await pc.setLocalDescription(answer);
  send({ type: 'answer', sdp: answer.sdp });
}

async function flip() {
  const previous = facing;
  facing = facing === 'user' ? 'environment' : 'user';
  try {
    const next = await openCamera();
    const track = next.getVideoTracks()[0];
    const sender = pc && pc.getSenders().find((s) => s.track && s.track.kind === 'video');
    if (sender) await sender.replaceTrack(track);
    localStream.getTracks().forEach((t) => t.stop());
    localStream = next;
    $('local').srcObject = localStream;
  } catch (err) {
    facing = previous;
  }
}

function fail(err) {
  console.error(err);
  end('Something went wrong: ' + (err && err.message ? err.message : err));
}

function end(text) {
  send({ type: 'bye' });
  if (ws) { ws.onclose = null; ws.close(); ws = null; }
  if (pc) { pc.onconnectionstatechange = null; pc.close(); pc = null; }
  if (localStream) { localStream.getTracks().forEach((t) => t.stop()); localStream = null; }
  $('remote').srcObject = null;
  $('local').srcObject = null;
  showStage(false);
  say(text);
  startBtn.disabled = true;
  startBtn.hidden = true;
}

startBtn.addEventListener('click', start);
$('flip').addEventListener('click', flip);
$('stop').addEventListener('click', () => end('Stopped. Scan the QR code on your computer to start again.'));
