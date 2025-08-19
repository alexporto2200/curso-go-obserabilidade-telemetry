package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/zipkin"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type CEPRequest struct {
	CEP string `json:"cep"`
}

type CEPResponse struct {
	City  string  `json:"city"`
	TempC float64 `json:"temp_C"`
	TempF float64 `json:"temp_F"`
	TempK float64 `json:"temp_K"`
}

var tracer trace.Tracer

func initTracer() {
	exporter, err := zipkin.New("http://zipkin:9411/api/v2/spans")
	if err != nil {
		log.Fatal(err)
	}

	batcher := sdktrace.NewBatchSpanProcessor(exporter)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(batcher),
	)
	otel.SetTracerProvider(tp)
	tracer = tp.Tracer("service-a")
}

func validateCEP(cep string) bool {
	// Verifica se tem exatamente 8 dígitos
	matched, _ := regexp.MatchString(`^\d{8}$`, cep)
	return matched
}

func handleCEP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, span := tracer.Start(r.Context(), "handle_cep_request")
	defer span.End()

	var req CEPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validação do CEP
	if !validateCEP(req.CEP) {
		log.Printf("CEP inválido: %s", req.CEP)
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte("invalid zipcode"))
		return
	}

	// Chama o Serviço B
	log.Printf("Encaminhando CEP %s para o Service B", req.CEP)
	serviceBURL := "http://service-b:8081/weather"
	requestBody, _ := json.Marshal(req)

	ctx, callSpan := tracer.Start(ctx, "call_service_b")
	defer callSpan.End()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", serviceBURL, bytes.NewBuffer(requestBody))
	if err != nil {
		log.Printf("Erro ao criar requisição: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		log.Printf("Erro ao chamar Service B: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// Repassa a resposta do Serviço B
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)

	if resp.StatusCode == http.StatusOK {
		var weatherResp CEPResponse
		if err := json.NewDecoder(resp.Body).Decode(&weatherResp); err != nil {
			http.Error(w, "Error decoding response", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(weatherResp)
	} else {
		// Repassa mensagem de erro
		body := make([]byte, 1024)
		n, _ := resp.Body.Read(body)
		w.Write(body[:n])
	}
}

func main() {
	initTracer()

	http.HandleFunc("/cep", handleCEP)

	log.Println("Service A iniciado na porta 8080")
	log.Println("Endpoint: /cep")
	log.Println("Zipkin: http://localhost:9411")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
