import assert from 'node:assert/strict';
import test from 'node:test';
import { clearRenewalDraft, renewalDraft, renewalDraftKey, renewalExpiry, renewalOrderInput, saveRenewalDraft } from '../src/portal/renewal-data.ts';

const template = { id:'template', revision:3, priceCents:1200, validityDays:30 };
const sub = { id:'subscription', revision:7, expiresAt:'2026-12-01T08:00:00Z', currentPeriodEndsAt:'2026-12-01T08:00:00Z' };
function storage() {
  const values = new Map();
  globalThis.sessionStorage = { getItem:key=>values.get(key) ?? null, setItem:(key,value)=>values.set(key,value), removeItem:key=>values.delete(key) };
  return values;
}

test('uncertain renewal retries immutable original parameters after template and subscription changes', () => {
  storage();
  const first = { ...renewalDraft('user', template, sub), attempted:true };
  saveRenewalDraft('user', first);
  const retry = renewalDraft('user', {...template, revision:4, priceCents:1800}, {...sub, revision:8});
  assert.deepEqual(renewalOrderInput(retry), {requestId:first.requestId, templateId:'template', templateRevision:3, renewalSubscriptionId:'subscription', expectedSubscriptionRevision:7});
  assert.equal(retry.template.priceCents, 1200);
  clearRenewalDraft('user',sub.id);
  assert.notEqual(renewalDraft('user',template,sub).requestId,first.requestId);
});

test('draft identity isolates users, subscription copies, and new purchases', () => {
  const values=storage();
  const first={...renewalDraft('user',template,sub),attempted:true};
  saveRenewalDraft('user',first);
  values.set('hqip:checkout:user:template','purchase-untouched');
  assert.notEqual(renewalDraft('other',template,sub).requestId,first.requestId);
  assert.notEqual(renewalDraft('user',template,{...sub,id:'another-copy'}).requestId,first.requestId);
  clearRenewalDraft('user',sub.id);
  assert.equal(values.get('hqip:checkout:user:template'),'purchase-untouched');
});

test('corrupt drafts are rejected and unavailable persistence fails before submission', () => {
  const values=storage();
  for(const value of ['{broken', JSON.stringify({attempted:true,requestId:'unsafe'})]) {
    values.set(renewalDraftKey('user',sub.id),value);
    assert.equal(renewalDraft('user',template,sub).attempted,false);
  }
  sessionStorage.setItem=()=>{throw new Error('storage blocked');};
  assert.throws(()=>saveRenewalDraft('user',renewalDraft('user',template,sub)),/blocked/);
});

test('expiry preview appends exact days to the prepaid end rather than today or month boundaries', () => {
  assert.equal(renewalExpiry('2026-12-31T08:30:00Z',30),'2027-01-30T08:30:00.000Z');
  assert.equal(renewalExpiry(renewalExpiry(sub.expiresAt,30),30),'2027-01-30T08:00:00.000Z');
});
