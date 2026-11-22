package orders

type APIError struct {
	Status        int
	Code, Message string
}

func (e *APIError) Error() string { return e.Code }
func unauthorized() *APIError {
	return &APIError{401, "unauthorized", "El acceso requiere credenciales válidas."}
}
func unavailable() *APIError {
	return &APIError{503, "service_unavailable", "No pudimos completar la consulta. Intenta de nuevo con la misma clave."}
}
func invalid() *APIError {
	return &APIError{400, "invalid_request", "Revisa los datos de la solicitud."}
}
func missing() *APIError   { return &APIError{404, "not_found", "No encontramos el recurso."} }
func forbidden() *APIError { return &APIError{403, "forbidden", "Tu rol no permite esta operación."} }

type UpstreamError struct {
	Status int
	Code   string
}

func (e *UpstreamError) Error() string { return "dependencia no disponible o respuesta no válida" }
