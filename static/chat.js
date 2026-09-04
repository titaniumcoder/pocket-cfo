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
  var submit = form.querySelector('button[type=submit]');

  function show(kind, text) {
    var box = document.createElement('div');
    box.className = 'msg ' + kind;
    var pre = document.createElement('pre');
    pre.textContent = text;
    box.appendChild(pre);
    transcript.appendChild(box);
    box.scrollIntoView({ block: 'end' });
  }

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

  var failed = false;

  function handle(line) {
    if (!line.trim()) return;
    var ev = JSON.parse(line);
    switch (ev.event) {
      case 'assistant':
        if (ev.message.content) show('assistant', ev.message.content);
        if (ev.message.tool_calls) {
          show('assistant calls', ev.message.tool_calls.map(function (c) { return c.function.name; }).join(', '));
        }
        break;
      case 'tool':
        show('tool', ev.message.name + ' → ' + (ev.message.content || '').slice(0, 400));
        break;
      case 'pending':
        status.textContent = ev.pending.error ? ev.pending.tool + ' failed: ' + ev.pending.error : 'Staged ' + ev.pending.tool + ' — waiting for your approval';
        break;
      case 'error':
        failed = true;
        status.textContent = ev.error;
        break;
      case 'done':
        status.textContent = 'Done';
        break;
    }
  }

  function pump(reader, decoder, buffer) {
    return reader.read().then(function (chunk) {
      if (chunk.done) { if (buffer) handle(buffer); return; }
      buffer += decoder.decode(chunk.value, { stream: true });
      var lines = buffer.split('\n');
      buffer = lines.pop();
      lines.forEach(handle);
      return pump(reader, decoder, buffer);
    });
  }

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    var text = form.elements.text.value;
    readFiles(form.elements.files).then(function (files) {
      if (!text.trim() && !files.length) return;
      submit.disabled = true;
      failed = false;
      show('user', text + (files.length ? '\n[attached: ' + files.map(function (f) { return f.name; }).join(', ') + ']' : ''));
      status.textContent = 'Thinking…';
      return fetch(form.action, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text: text, files: files })
      }).then(function (resp) {
        if (!resp.ok) return resp.text().then(function (t) { throw new Error(t || ('HTTP ' + resp.status)); });
        return pump(resp.body.getReader(), new TextDecoder(), '');
      }).then(function () {
        if (!failed) location.reload();
        submit.disabled = false;
      });
    }).catch(function (err) {
      failed = true;
      status.textContent = 'Failed: ' + err.message;
      submit.disabled = false;
    });
  });
})();
