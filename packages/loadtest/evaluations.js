import http from "k6/http";
import { check } from "k6";
import { Endpoint, SignatureV4 } from "https://jslib.k6.io/aws/0.14.0/signature.js";

const base = __ENV.BASE_URL;
if (!base) {
  throw new Error("BASE_URL is required (make api-url after local-deploy)");
}

// Local Floci runs default to 100 req/s; the 1000 req/s NFR run targets a
// real AWS stack with LOADTEST_RATE=1000.
const rate = Number(__ENV.LOADTEST_RATE || 100);
if (!Number.isInteger(rate) || rate <= 0) {
  throw new Error(`LOADTEST_RATE must be a positive integer, got "${__ENV.LOADTEST_RATE}"`);
}

const customers = [
  { name: "Ana", cpf: "39053344705", credit_score: 780, current_invoice_cents: 50000, credit_limit_cents: 500000, late_payments: 0, monthly_spend_cents: [80000, 90000, 70000] },
  { name: "Eva", cpf: "22233344405", credit_score: 820, current_invoice_cents: 100000, credit_limit_cents: 1000000, late_payments: 0, monthly_spend_cents: [100000, 110000, 90000] },
  { name: "Fábio", cpf: "33344455508", credit_score: 650, current_invoice_cents: 40000, credit_limit_cents: 400000, late_payments: 1, monthly_spend_cents: [50000, 60000, 55000] },
  { name: "Bruno", cpf: "12345678909", credit_score: 520, current_invoice_cents: 200000, credit_limit_cents: 400000, late_payments: 0, monthly_spend_cents: [80000] },
  { name: "Diego", cpf: "11122233396", credit_score: 810, current_invoice_cents: 50000, credit_limit_cents: 1200000, late_payments: 3, monthly_spend_cents: [200000, 180000, 190000] },
  { name: "Carla", cpf: "98765432100", credit_score: 690, current_invoice_cents: 900000, credit_limit_cents: 500000, late_payments: 1, monthly_spend_cents: [100000, 120000, 110000] },
  { name: "Helena", cpf: "44455566619", credit_score: 740, current_invoice_cents: 10000, credit_limit_cents: 300000, late_payments: 0, monthly_spend_cents: [] },
  { name: "Igor", cpf: "55566677720", credit_score: 710, current_invoice_cents: 10000, credit_limit_cents: 100000, late_payments: 0, monthly_spend_cents: [95000, 96000, 97000] },
  { name: "Joana", cpf: "66677788830", credit_score: 750, current_invoice_cents: 0, credit_limit_cents: 0, late_payments: 0, monthly_spend_cents: [10000] },
  { name: "Kai", cpf: "77788899941", credit_score: 800, current_invoice_cents: 20000, credit_limit_cents: 600000, late_payments: 0, monthly_spend_cents: [80000, 70000, 75000] },
];
const batch = JSON.stringify(customers);

const { protocol, host, pathPrefix } = splitBase(base);
const signer = new SignatureV4({
  service: "execute-api",
  region: __ENV.AWS_DEFAULT_REGION || __ENV.AWS_REGION || "us-east-1",
  credentials: {
    accessKeyId: __ENV.AWS_ACCESS_KEY_ID,
    secretAccessKey: __ENV.AWS_SECRET_ACCESS_KEY,
    sessionToken: __ENV.AWS_SESSION_TOKEN,
  },
  uriEscapePath: false,
  applyChecksum: true,
});

const floci = /:4566\b|floci/i.test(base);
// Floci starts one container per concurrent invoke; keep local VUs at the
// reserved Lambda cap. Real AWS scales with the arrival rate.
const maxVUs = floci ? 8 : Math.ceil(rate * 0.8);
const preAllocatedVUs = floci ? 8 : Math.ceil(rate * 0.3);

export const options = {
  scenarios: {
    nfr: {
      executor: "constant-arrival-rate",
      rate,
      timeUnit: "1s",
      duration: "10s",
      preAllocatedVUs,
      maxVUs,
    },
  },
  // Floci cannot hold the 800ms p99 of real Lambda; local gate is p95 and errors.
  thresholds: floci
    ? {
        http_req_duration: ["p(95)<2000"],
        http_req_failed: ["rate<0.01"],
      }
    : {
        http_req_duration: ["p(99)<800", "p(95)<1000"],
        http_req_failed: ["rate<0.01"],
      },
};

export default function () {
  const signed = signer.sign({
    method: "POST",
    endpoint: new Endpoint(`${protocol}://${host}`),
    path: `${pathPrefix}/evaluations/batch`,
    headers: { "content-type": "application/json" },
    body: batch,
  });
  const res = http.post(signed.url, signed.body, { headers: signed.headers });
  check(res, {
    "status 202": (r) => r.status === 202,
    queued: (r) => r.json("queued") === customers.length,
    batch_id: (r) => Boolean(r.json("batch_id")),
  });
}

function splitBase(raw) {
  const noQuery = raw.split("?")[0];
  const protoEnd = noQuery.indexOf("://");
  const protocol = noQuery.slice(0, protoEnd);
  const rest = noQuery.slice(protoEnd + 3);
  const slash = rest.indexOf("/");
  const host = slash === -1 ? rest : rest.slice(0, slash);
  const pathPrefix = (slash === -1 ? "" : rest.slice(slash)).replace(/\/$/, "");
  return { protocol, host, pathPrefix };
}
