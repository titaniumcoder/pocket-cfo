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

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    var text = form.elements.text.value;
    readFiles(form.elements.files).then(function (files) {
      if (!text.trim() && !files.length) return;
      setRunning(true);
      show('user', text + (files.length ? '\n[attached: ' + files.map(function (f) { return f.name; }).join(', ') + ']' : ''));
      status.textContent = 'Thinking…';
      return fetch(form.action, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text: text, files: files })
      }).then(function (resp) {
        if (!resp.ok) return resp.text().then(function (t) { throw new Error(t || ('HTTP ' + resp.status)); });
        form.elements.text.value = '';
        form.elements.files.value = '';
        follow(0);
      });
    }).catch(function (err) {
      status.textContent = 'Failed: ' + err.message;
      setRunning(false);
    });
  });
})();
