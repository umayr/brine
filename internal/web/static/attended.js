// Explicit review and confirmation; never retry or reconstruct a token from history.
let attendedGeneration = 0;
async function attendedReview(action, name, output) {
  const generation = ++attendedGeneration;
  output.classList.remove('hidden');
  const log = document.getElementById('action-log');
  const title = document.getElementById('action-title');
  title.textContent = 'Review ' + action + ' ' + name;
  log.textContent = 'Requesting preview…';
  let controls = document.getElementById('attended-confirm');
  if (!controls) {
    controls = document.createElement('div'); controls.id = 'attended-confirm';
    output.appendChild(controls);
  }
  controls.replaceChildren();
  try {
    const res = await fetch('/api/stacks/' + encodeURIComponent(name) + '/operations/preview', {
      method: 'POST', headers: brineHeaders({'Content-Type': 'application/json'}), body: JSON.stringify({action}),
    });
    if (generation !== attendedGeneration) return;
    const requestId = res.headers.get('X-Brine-Request-ID') || '';
    BrineUI.operationLookup(document, output.parentNode, requestId, true);
    if (!res.ok) throw new Error('Preview unavailable. Inspect the request before a new attempt.');
    const review = await res.json();
    if (generation !== attendedGeneration) return;
    if (review.requestId !== requestId || !review.confirmationToken || !review.reviewDigest) throw new Error('Invalid preview.');
    log.textContent = [
      'Review all resources, identity kinds, coverage, diagnostics, and source/context references below. Source references are assertions, not proof.',
      'Present IDs identify inspected resources; planned names are not future IDs or promises. Recreated/removed containers lose their writable layer.',
      'Named-volume retention is not a backup or proof of contents or incarnation. No volume-consumer proof or native qualification is implied.',
      'Runtime facts may vary. No reservation, transaction, rollback, or predicted success is promised. Up may ensure/start/network even for a config no-op.',
      JSON.stringify(review.review, null, 2),
    ].join('\n\n');
    const label = document.createElement('label');
    const checkbox = document.createElement('input'); checkbox.type = 'checkbox';
    label.appendChild(checkbox); label.appendChild(document.createTextNode(' I reviewed this action and its complete context.'));
    const button = document.createElement('button'); button.type = 'button'; button.textContent = 'Confirm once';
    controls.appendChild(label); controls.appendChild(button);
    button.addEventListener('click', async () => {
      if (!checkbox.checked || button.disabled || generation !== attendedGeneration) return;
      button.disabled = true; checkbox.disabled = true;
      // Consume locally before the request; a failure always requires a new preview.
      const token = review.confirmationToken; review.confirmationToken = '';
      try {
        const apply = await fetch('/api/stacks/' + encodeURIComponent(name) + '/' + action, {
          method: 'POST', headers: brineHeaders({'Content-Type': 'application/json'}),
          body: JSON.stringify({requestId: review.requestId, reviewDigest: review.reviewDigest, confirmationToken: token}),
        });
        const id = apply.headers.get('X-Brine-Request-ID') || '';
        BrineUI.operationLookup(document, output.parentNode, id, true);
        const result = await apply.text();
        log.textContent = 'Delivery is not acknowledgement or proof of success. Inspect durable history before any new attempt.\n' + result;
      } catch { log.textContent = 'Delivery unknown. Inspect the same request before a new preview; no retry was sent.'; }
    });
  } catch (err) { if (generation === attendedGeneration) log.textContent = String(err.message || 'Preview unavailable.'); }
}
