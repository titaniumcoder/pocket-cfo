// The chat page. A turn runs on the server whether or not this page is
// watching; the page only starts it and follows its events, then reloads so
// it shows exactly what was saved. Nothing here holds state a reload would
// lose. CSP forbids inline scripts, which is why every handler lives here.
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
  var textarea = form.elements.text;
  var fileInput = document.getElementById('composer-files');
  var attach = document.getElementById('attach');
  var chips = document.getElementById('chips');
  var send = form.querySelector('button[type=submit]');
  var files = [];

  function setRunning(running) {
    [textarea, fileInput, attach, send].forEach(function (el) { el.disabled = running; });
  }

  function grow() {
    textarea.style.height = 'auto';
    textarea.style.height = Math.min(textarea.scrollHeight, 220) + 'px';
  }
  textarea.addEventListener('input', grow);
  textarea.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      form.requestSubmit();
    }
  });

  function kb(n) { return n < 1024 ? n + ' B' : (n / 1024).toFixed(1) + ' KB'; }
  function renderChips() {
    chips.innerHTML = '';
    files.forEach(function (file, i) {
      var chip = document.createElement('span');
      chip.className = 'chip';
      var label = document.createElement('span');
      label.textContent = file.name + ' · ' + kb(file.size);
      var remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'chip-remove';
      remove.setAttribute('aria-label', 'Remove ' + file.name);
      remove.textContent = '×';
      remove.addEventListener('click', function () { files.splice(i, 1); renderChips(); });
      chip.appendChild(label);
      chip.appendChild(remove);
      chips.appendChild(chip);
    });
  }
  attach.addEventListener('click', function () { fileInput.click(); });
  fileInput.addEventListener('change', function () {
    Array.prototype.forEach.call(fileInput.files, function (f) { files.push(f); });
    fileInput.value = '';
    renderChips();
  });

  function readFiles() {
    return Promise.all(files.map(function (file) {
      return new Promise(function (resolve, reject) {
        var reader = new FileReader();
        reader.onload = function () { resolve({ name: file.name, content: reader.result }); };
        reader.onerror = function () { reject(new Error('could not read ' + file.name)); };
        reader.readAsText(file);
      });
    }));
  }

  function scrollToEnd(el) { el.scrollIntoView({ block: 'end' }); }

  function show(kind, text) {
    var box = document.createElement('div');
    box.className = 'msg ' + kind;
    var pre = document.createElement('pre');
    pre.textContent = text;
    box.appendChild(pre);
    transcript.appendChild(box);
    scrollToEnd(box);
    return box;
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
    [['log-kind', kind], ['log-name', name], ['log-text', text]].forEach(function (part) {
      var span = document.createElement('span');
      span.className = part[0];
      span.textContent = part[1];
      line.appendChild(span);
    });
    logBox.appendChild(line);
    scrollToEnd(line);
  }
  function excerpt(s) { s = (s || '').replace(/\s+/g, ' '); return s.length > 240 ? s.slice(0, 240) + '…' : s; }

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

  if (form.dataset.running === 'true') {
    setRunning(true);
    follow(0);
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
    var text = textarea.value;
    if (!text.trim() && !files.length) return;
    readFiles().then(function (uploads) {
      show('user', text + (uploads.length ? '\n[attached: ' + uploads.map(function (f) { return f.name; }).join(', ') + ']' : ''));
      textarea.value = '';
      grow();
      files = [];
      renderChips();
      return start({ text: text, files: uploads });
    }).catch(function (err) {
      status.textContent = 'Failed: ' + err.message;
      setRunning(false);
    });
  });

  var last = transcript.lastElementChild;
  if (last) last.scrollIntoView({ block: 'end' });
})();
