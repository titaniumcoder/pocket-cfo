// The chat page: sends a turn as JSON, renders the NDJSON events as they
// stream in, and reloads once the turn is over so the page shows exactly what
// the server persisted. The page holds no state of its own — everything a
// reload needs is in the chat file on the server.
(function () {
  var changes = document.getElementById('changes');
  if (changes) {
    var changesStatus = document.getElementById('changes-status');
    changes.addEventListener('click', function (e) {
      var button = e.target.closest('.chat-action');
      if (!button) return;
      if (button.dataset.confirm && !window.confirm(button.dataset.confirm)) return;
      button.disabled = true;
      changesStatus.textContent = 'Working…';
      fetch('/chat/' + changes.dataset.chat + '/' + button.dataset.action, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ index: Number(button.dataset.index || 0) })
      }).then(function (resp) {
        if (resp.ok) { location.reload(); return; }
        return resp.text().then(function (t) {
          var message = t;
          try { message = JSON.parse(t).error.message; } catch (err) { /* plain text */ }
          throw new Error(message || ('HTTP ' + resp.status));
        });
      }).catch(function (err) {
        changesStatus.textContent = 'Failed: ' + err.message;
        button.disabled = false;
      });
    });
  }

  var form = document.getElementById('composer');
  if (!form) return;
  var transcript = document.getElementById('transcript');
  var status = document.getElementById('turn-status');
  var controls = [form.elements.text, form.elements.files, form.querySelector('button[type=submit]')];

  function setRunning(running) {
    controls.forEach(function (el) { el.disabled = running; });
  }

  function show(kind, text) {
    var box = document.createElement('div');
    box.className = 'msg ' + kind;
    var pre = document.createElement('pre');
    pre.textContent = text;
    box.appendChild(pre);
    transcript.appendChild(box);
    box.scrollIntoView({ block: 'end' });
  }

  var logBox = null;
  function log(kind, name, text) {
    if (!logBox) {
      logBox = document.createElement('div');
      logBox.className = 'msg logs';
      transcript.appendChild(logBox);
    }
    var line = document.createElement('div');
    line.className = 'log';
    var k = document.createElement('span'); k.className = 'log-kind'; k.textContent = kind;
    var n = document.createElement('span'); n.className = 'log-name'; n.textContent = name;
    var t = document.createElement('span'); t.className = 'log-text'; t.textContent = text;
    line.appendChild(k); line.appendChild(n); line.appendChild(t);
    logBox.appendChild(line);
    line.scrollIntoView({ block: 'end' });
  }
  function excerpt(s) { s = (s || '').replace(/\s+/g, ' '); return s.length > 240 ? s.slice(0, 240) + '…' : s; }

  function readFiles(input) {
    return Promise.all(Array.prototype.map.call(input.files, function (file) {
      return new Promise(function (resolve, reject) {
        var reader = new FileReader();
        reader.onload = function () { resolve({ name: file.name, content: reader.result }); };
        reader.onerror = function () { reject(new Error('could not read ' + file.name)); };
        reader.readAsText(file);
      });
    }));
  }

  // The turn runs on the server whether or not this page is watching; the
  // page only follows it. Reloading on "done" renders exactly what was saved.
  function follow(since) {
    var source = new EventSource(form.dataset.events + '?since=' + since);
    source.onmessage = function (e) {
      var ev = JSON.parse(e.data);
      switch (ev.event) {
        case 'assistant':
          if (ev.message.reasoning) log('thinking', '', excerpt(ev.message.reasoning));
          if (ev.message.tool_calls) {
            ev.message.tool_calls.forEach(function (c) { log('call', c.function.name, excerpt(c.function.arguments)); });
          } else if (ev.message.content) {
            logBox = null;
            show('answer', ev.message.content);
          }
          break;
        case 'tool':
          log('→', ev.message.name, excerpt(ev.message.content));
          break;
        case 'question':
          status.textContent = 'Waiting for your answer';
          break;
        case 'pending':
          status.textContent = ev.pending.error ? ev.pending.tool + ' failed: ' + ev.pending.error : 'Staged ' + ev.pending.tool + ' — waiting for your approval';
          break;
        case 'error':
          source.close();
          status.textContent = ev.error;
          setRunning(false);
          break;
        case 'done':
          source.close();
          status.textContent = 'Done';
          location.reload();
          break;
      }
    };
  }

  if (form.dataset.running === 'true') {
    setRunning(true);
    follow(0);
  }

  function start(payload) {
    setRunning(true);
    status.textContent = 'Thinking…';
    return fetch(form.action, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    }).then(function (resp) {
      if (!resp.ok) return resp.text().then(function (t) { throw new Error(t || ('HTTP ' + resp.status)); });
      follow(0);
    }).catch(function (err) {
      status.textContent = 'Failed: ' + err.message;
      setRunning(false);
    });
  }

  var question = document.getElementById('question');
  if (question) {
    var answer = function (text) {
      if (!text.trim()) return;
      question.querySelectorAll('button').forEach(function (b) { b.disabled = true; });
      show('user', text);
      start({ answer: { tool_call_id: question.dataset.call, text: text } });
    };
    question.addEventListener('click', function (e) {
      var option = e.target.closest('.question-option');
      if (option) answer(option.dataset.answer);
      if (e.target.closest('.question-send')) answer(document.getElementById('question-free').value);
    });
    var free = document.getElementById('question-free');
    if (free) free.addEventListener('keydown', function (e) { if (e.key === 'Enter') { e.preventDefault(); answer(free.value); } });
  }

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    var text = form.elements.text.value;
    readFiles(form.elements.files).then(function (files) {
      if (!text.trim() && !files.length) return;
      show('user', text + (files.length ? '\n[attached: ' + files.map(function (f) { return f.name; }).join(', ') + ']' : ''));
      form.elements.text.value = '';
      form.elements.files.value = '';
      return start({ text: text, files: files });
    }).catch(function (err) {
      status.textContent = 'Failed: ' + err.message;
      setRunning(false);
    });
  });
})();
